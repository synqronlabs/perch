package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// FlagsOp is a STORE operation.
type FlagsOp int

const (
	FlagsSet FlagsOp = iota
	FlagsAdd
	FlagsDel
)

// StoreFlags updates system flags and keywords on messages.
func (s *Store) StoreFlags(ctx context.Context, mailboxID int64, uids []uint32, op FlagsOp, flags Flags, keywords []string) ([]Message, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin store flags: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var highest uint64
	if err := tx.QueryRowContext(ctx, `SELECT highest_modseq FROM mailboxes WHERE id = ?`, mailboxID).Scan(&highest); err != nil {
		return nil, fmt.Errorf("read mailbox modseq: %w", err)
	}
	highest++
	if _, err := tx.ExecContext(ctx, `UPDATE mailboxes SET highest_modseq = ? WHERE id = ?`, highest, mailboxID); err != nil {
		return nil, fmt.Errorf("advance mailbox modseq: %w", err)
	}
	for _, uid := range uids {
		var messageID int64
		var current Flags
		err := tx.QueryRowContext(ctx, `SELECT m.id, COALESCE(f.flags,0) FROM messages m
			LEFT JOIN message_flags f ON f.message_id = m.id WHERE m.mailbox_id = ? AND m.uid = ?`, mailboxID, uid).
			Scan(&messageID, &current)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read message flags: %w", err)
		}
		next := applyFlags(current, op, flags)
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_flags (message_id, flags) VALUES (?, ?)
			ON CONFLICT(message_id) DO UPDATE SET flags = excluded.flags`, messageID, uint32(next)); err != nil {
			return nil, fmt.Errorf("update message flags: %w", err)
		}
		if op == FlagsSet {
			if _, err := tx.ExecContext(ctx, `DELETE FROM message_keywords WHERE message_id = ?`, messageID); err != nil {
				return nil, fmt.Errorf("clear keywords: %w", err)
			}
		}
		for _, keyword := range keywords {
			keyword = strings.TrimSpace(keyword)
			if keyword == "" {
				continue
			}
			switch op {
			case FlagsDel:
				if _, err := tx.ExecContext(ctx, `DELETE FROM message_keywords WHERE message_id = ? AND keyword = ? COLLATE NOCASE`, messageID, keyword); err != nil {
					return nil, fmt.Errorf("delete keyword: %w", err)
				}
			default:
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_keywords (message_id, keyword) VALUES (?, ?)`, messageID, keyword); err != nil {
					return nil, fmt.Errorf("add keyword: %w", err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET modseq = ? WHERE id = ?`, highest, messageID); err != nil {
			return nil, fmt.Errorf("update message modseq: %w", err)
		}
	}
	updated, err := messagesForUIDs(ctx, tx, mailboxID, uids)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit store flags: %w", err)
	}
	return updated, nil
}

func applyFlags(current Flags, op FlagsOp, flags Flags) Flags {
	switch op {
	case FlagsSet:
		return flags
	case FlagsAdd:
		return current | flags
	case FlagsDel:
		return current &^ flags
	default:
		return current
	}
}

func messagesForUIDs(ctx context.Context, q rowQueryer, mailboxID int64, uids []uint32) ([]Message, error) {
	var out []Message
	for _, chunk := range chunks(uids, 500) {
		messages, err := queryMessages(ctx, q, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.uid IN (`+placeholders(len(chunk))+`) ORDER BY m.uid`,
			argsWith(mailboxID, chunk)...)
		if err != nil {
			return nil, fmt.Errorf("query updated messages: %w", err)
		}
		out = append(out, messages...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// Expunge deletes messages with \Deleted set, optionally restricted to uids.
// It returns the expunged UIDs in ascending order.

// Expunge deletes messages with \Deleted set, optionally restricted to uids.
// It returns the expunged UIDs in ascending order.
func (s *Store) Expunge(ctx context.Context, mailboxID int64, uids []uint32) ([]uint32, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin expunge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	deleted, err := expungeTx(ctx, tx, mailboxID, uids, true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit expunge: %w", err)
	}
	return deleted, nil
}

func expungeTx(ctx context.Context, tx *sql.Tx, mailboxID int64, uids []uint32, requireDeleted bool) ([]uint32, error) {
	query := `SELECT m.id, m.uid, m.blob_id FROM messages m
		LEFT JOIN message_flags f ON f.message_id = m.id WHERE m.mailbox_id = ?`
	args := []any{mailboxID}
	if requireDeleted {
		query += ` AND (COALESCE(f.flags,0) & ?) <> 0`
		args = append(args, uint32(FlagDeleted))
	}
	query += ` ORDER BY m.uid`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list expunge messages: %w", err)
	}
	type target struct {
		id, blob int64
		uid      uint32
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.uid, &t.blob); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan expunge message: %w", err)
		}
		if len(uids) > 0 && !slices.Contains(uids, t.uid) {
			continue
		}
		targets = append(targets, t)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close expunge messages: %w", err)
	}
	var deleted []uint32
	for _, t := range targets {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, t.id); err != nil {
			return nil, fmt.Errorf("delete message: %w", err)
		}
		if err := releaseBlob(ctx, tx, t.blob); err != nil {
			return nil, err
		}
		deleted = append(deleted, t.uid)
	}
	sort.Slice(deleted, func(i, j int) bool { return deleted[i] < deleted[j] })
	return deleted, nil
}

func releaseBlob(ctx context.Context, tx *sql.Tx, blobID int64) error {
	var kind string
	var refcount int64
	if err := tx.QueryRowContext(ctx, `SELECT kind, refcount FROM blobs WHERE id = ?`, blobID).Scan(&kind, &refcount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read blob refcount: %w", err)
	}
	refcount--
	if refcount > 0 {
		_, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = ? WHERE id = ?`, refcount, blobID)
		return err
	}
	if kind == "s3" {
		// Keep the row as a tombstone so reconciliation does not re-ingest a
		// message the user deleted. The remote archive is intentionally kept.
		_, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = 0 WHERE id = ?`, blobID)
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, blobID); err != nil {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}
