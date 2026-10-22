package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// CopyResult reports a COPY or MOVE.
type CopyResult struct {
	SourceUIDs  []uint32
	DestUIDs    []uint32
	UIDValidity uint32
}

// CopyMessages copies messages to destMailbox in one transaction.

// CopyMessages copies messages to destMailbox in one transaction.
func (s *Store) CopyMessages(ctx context.Context, sourceMailboxID, destMailboxID int64, uids []uint32) (CopyResult, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return CopyResult{}, fmt.Errorf("begin copy: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := copyMessagesTx(ctx, tx, sourceMailboxID, destMailboxID, uids)
	if err != nil {
		return CopyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CopyResult{}, fmt.Errorf("commit copy: %w", err)
	}
	return result, nil
}

// MoveMessages copies messages and removes the source rows atomically.

// MoveMessages copies messages and removes the source rows atomically.
func (s *Store) MoveMessages(ctx context.Context, sourceMailboxID, destMailboxID int64, uids []uint32) (CopyResult, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return CopyResult{}, fmt.Errorf("begin move: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := copyMessagesTx(ctx, tx, sourceMailboxID, destMailboxID, uids)
	if err != nil {
		return CopyResult{}, err
	}
	if _, err := expungeTx(ctx, tx, sourceMailboxID, uids, false); err != nil {
		return CopyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CopyResult{}, fmt.Errorf("commit move: %w", err)
	}
	return result, nil
}

func copyMessagesTx(ctx context.Context, tx *sql.Tx, sourceMailboxID, destMailboxID int64, uids []uint32) (CopyResult, error) {
	if len(uids) == 0 {
		return CopyResult{}, nil
	}
	var destAccount int64
	var uidNext uint32
	var highest uint64
	var uidValidity uint32
	if err := tx.QueryRowContext(ctx, `SELECT account_id, uid_validity, uid_next, highest_modseq FROM mailboxes WHERE id = ?`, destMailboxID).
		Scan(&destAccount, &uidValidity, &uidNext, &highest); err != nil {
		return CopyResult{}, fmt.Errorf("read destination mailbox: %w", err)
	}
	var sources []Message
	for _, chunk := range chunks(uids, 500) {
		messages, err := queryMessages(ctx, tx, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.uid IN (`+placeholders(len(chunk))+`) ORDER BY m.uid`,
			argsWith(sourceMailboxID, chunk)...)
		if err != nil {
			return CopyResult{}, fmt.Errorf("read source messages: %w", err)
		}
		sources = append(sources, messages...)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].UID < sources[j].UID })
	if len(sources) == 0 {
		return CopyResult{UIDValidity: uidValidity}, nil
	}
	now := time.Now().UTC().Unix()
	result := CopyResult{UIDValidity: uidValidity}
	for _, src := range sources {
		uid := uidNext
		uidNext++
		highest++
		res, err := tx.ExecContext(ctx, `
			INSERT INTO messages (
				account_id, mailbox_id, uid, modseq, blob_id, size, internal_date,
				message_id, subject, from_json, to_json, cc_json, bcc_json, reply_to_json,
				in_reply_to_json, references_json, sent_at, headers_json, mime_json,
				raw_sha256, s3_key, mxr_task_id, mxr_correlation_id, is_spam, created_at
			)
			SELECT ?, ?, ?, ?, blob_id, size, internal_date, message_id, subject, from_json, to_json,
				cc_json, bcc_json, reply_to_json, in_reply_to_json, references_json, sent_at,
				headers_json, mime_json, raw_sha256, s3_key, NULL, NULL, is_spam, ?
			FROM messages WHERE id = ?`, destAccount, destMailboxID, uid, highest, now, src.ID)
		if err != nil {
			return CopyResult{}, fmt.Errorf("copy message %d: %w", src.UID, err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return CopyResult{}, fmt.Errorf("copied message id: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_flags (message_id, flags) VALUES (?, ?)`, newID, uint32(src.Flags)); err != nil {
			return CopyResult{}, fmt.Errorf("copy flags: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_keywords (message_id, keyword)
			SELECT ?, keyword FROM message_keywords WHERE message_id = ?`, newID, src.ID); err != nil {
			return CopyResult{}, fmt.Errorf("copy keywords: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO search_content
			(message_id, subject, sender, recipients, headers, body, attachments)
			SELECT ?, subject, sender, recipients, headers, body, attachments FROM search_content WHERE message_id = ?`,
			newID, src.ID); err != nil {
			return CopyResult{}, fmt.Errorf("copy search content: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, src.BlobID); err != nil {
			return CopyResult{}, fmt.Errorf("copy blob ref: %w", err)
		}
		result.SourceUIDs = append(result.SourceUIDs, src.UID)
		result.DestUIDs = append(result.DestUIDs, uid)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mailboxes SET uid_next = ?, highest_modseq = ? WHERE id = ?`,
		uidNext, highest, destMailboxID); err != nil {
		return CopyResult{}, fmt.Errorf("update destination counters: %w", err)
	}
	return result, nil
}

// FlagsOp is a STORE operation.
