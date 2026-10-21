package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Outbound is one submitted message and its latest delivery status.
type Outbound struct {
	ID             int64
	AccountID      int64
	MessageRef     string
	TaskID         string
	FromAddress    string
	Recipients     []string
	Subject        string
	Size           int64
	Status         string
	Attempt        uint64
	SMTPCode       int
	EnhancedStatus string
	RemoteResponse string
	Reason         string
	AcceptedAt     time.Time
	OccurredAt     time.Time
	NextRetryAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// UpsertOutboundAccepted records the submission acceptance row.
func (s *Store) UpsertOutboundAccepted(ctx context.Context, out Outbound) error {
	if out.MessageRef == "" && out.TaskID == "" {
		return errors.New("store: outbound requires message_ref or task_id")
	}
	recipients, _ := json.Marshal(out.Recipients)
	now := time.Now().UTC()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbound accepted: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	id, err := findOutboundTx(ctx, tx, out.MessageRef, out.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO outbound (account_id, message_ref, task_id, from_address, recipients_json, subject, size,
				status, accepted_at, occurred_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullInt64(out.AccountID), out.MessageRef, out.TaskID, out.FromAddress, string(recipients), out.Subject, out.Size,
			firstStatus(out.Status), unixOrNow(out.AcceptedAt), unixOrNow(out.OccurredAt), now.Unix(), now.Unix())
		if err != nil {
			return fmt.Errorf("insert outbound: %w", err)
		}
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("lookup outbound: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE outbound SET account_id = COALESCE(?, account_id), message_ref = ?, task_id = ?,
			from_address = ?, recipients_json = ?, subject = ?, size = ?,
			accepted_at = ?, occurred_at = ?, updated_at = ?
		WHERE id = ?`,
		nullInt64(out.AccountID), out.MessageRef, out.TaskID, out.FromAddress, string(recipients), out.Subject, out.Size,
		unixOrNow(out.AcceptedAt), unixOrNow(out.OccurredAt), now.Unix(), id)
	if err != nil {
		return fmt.Errorf("update outbound accepted: %w", err)
	}
	return tx.Commit()
}

// RecordOutboundAttempt updates the latest attempt status for a submission.
func (s *Store) RecordOutboundAttempt(ctx context.Context, out Outbound) error {
	if out.MessageRef == "" && out.TaskID == "" {
		return errors.New("store: outbound attempt requires message_ref or task_id")
	}
	now := time.Now().UTC()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbound attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	id, err := findOutboundTx(ctx, tx, out.MessageRef, out.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		recipients, _ := json.Marshal(out.Recipients)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO outbound (account_id, message_ref, task_id, from_address, recipients_json, subject, size,
				status, attempt, smtp_code, enhanced_status, remote_response, reason,
				accepted_at, occurred_at, next_retry_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullInt64(out.AccountID), out.MessageRef, out.TaskID, out.FromAddress, string(recipients), out.Subject, out.Size,
			firstStatus(out.Status), out.Attempt, out.SMTPCode, out.EnhancedStatus, out.RemoteResponse, out.Reason,
			unixOrNow(out.AcceptedAt), unixOrNow(out.OccurredAt), unixOrZero(out.NextRetryAt), now.Unix(), now.Unix())
		if err != nil {
			return fmt.Errorf("insert outbound attempt: %w", err)
		}
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("lookup outbound attempt: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE outbound SET
			message_ref = CASE WHEN ? <> '' THEN ? ELSE message_ref END,
			task_id = CASE WHEN ? <> '' THEN ? ELSE task_id END,
			status = ?, attempt = ?, smtp_code = ?, enhanced_status = ?, remote_response = ?, reason = ?,
			accepted_at = CASE WHEN ? > 0 THEN ? ELSE accepted_at END,
			occurred_at = CASE WHEN ? > 0 THEN ? ELSE occurred_at END,
			next_retry_at = ?, updated_at = ?
		WHERE id = ?`,
		out.MessageRef, out.MessageRef, out.TaskID, out.TaskID,
		firstStatus(out.Status), out.Attempt, out.SMTPCode, out.EnhancedStatus, out.RemoteResponse, out.Reason,
		out.AcceptedAt.Unix(), out.AcceptedAt.Unix(), out.OccurredAt.Unix(), out.OccurredAt.Unix(),
		unixOrZero(out.NextRetryAt), now.Unix(), id)
	if err != nil {
		return fmt.Errorf("update outbound attempt: %w", err)
	}
	return tx.Commit()
}

// ListOutbound returns recent outbound messages, optionally filtered by account.
func (s *Store) ListOutbound(ctx context.Context, accountID int64, limit int) ([]Outbound, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT id, COALESCE(account_id,0), message_ref, task_id, from_address, recipients_json, subject, size,
		status, attempt, smtp_code, enhanced_status, remote_response, reason,
		accepted_at, occurred_at, next_retry_at, created_at, updated_at FROM outbound`
	args := []any{}
	if accountID > 0 {
		query += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list outbound: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Outbound
	for rows.Next() {
		var o Outbound
		var recipients string
		var accepted, occurred, retry, created, updated int64
		if err := rows.Scan(&o.ID, &o.AccountID, &o.MessageRef, &o.TaskID, &o.FromAddress, &recipients, &o.Subject, &o.Size,
			&o.Status, &o.Attempt, &o.SMTPCode, &o.EnhancedStatus, &o.RemoteResponse, &o.Reason,
			&accepted, &occurred, &retry, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan outbound: %w", err)
		}
		_ = json.Unmarshal([]byte(recipients), &o.Recipients)
		o.AcceptedAt = unixTime(accepted)
		o.OccurredAt = unixTime(occurred)
		o.NextRetryAt = unixTime(retry)
		o.CreatedAt = unixTime(created)
		o.UpdatedAt = unixTime(updated)
		out = append(out, o)
	}
	return out, rows.Err()
}

func findOutboundTx(ctx context.Context, tx *sql.Tx, messageRef, taskID string) (int64, error) {
	var id int64
	if messageRef != "" {
		err := tx.QueryRowContext(ctx, `SELECT id FROM outbound WHERE message_ref = ? LIMIT 1`, messageRef).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if taskID != "" {
		return id, tx.QueryRowContext(ctx, `SELECT id FROM outbound WHERE task_id = ? ORDER BY id DESC LIMIT 1`, taskID).Scan(&id)
	}
	return 0, sql.ErrNoRows
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func unixOrNow(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().UTC().Unix()
	}
	return t.UTC().Unix()
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func firstStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "accepted"
	}
	return status
}
