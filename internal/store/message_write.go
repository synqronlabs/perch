package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// InsertMessage stores a message and its blob, assigning the next UID. It is
// idempotent for non-empty TaskID: a duplicate returns the existing message
// with inserted=false.
func (s *Store) InsertMessage(ctx context.Context, in MessageInput) (Message, bool, error) {
	if in.Raw == nil && in.Kind == "local" {
		return Message{}, false, errors.New("store: local blob requires raw message bytes")
	}
	if in.InternalDate.IsZero() {
		in.InternalDate = time.Now().UTC()
	}
	if in.Size == 0 && in.Raw != nil {
		in.Size = int64(len(in.Raw))
	}
	if in.RawSHA256 == "" && in.Raw != nil {
		sum := sha256.Sum256(in.Raw)
		in.RawSHA256 = hex.EncodeToString(sum[:])
	}
	if in.Kind == "" {
		if in.S3Key != "" {
			in.Kind = "s3"
		} else {
			in.Kind = "local"
		}
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, fmt.Errorf("begin insert message: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if in.TaskID != "" {
		var existingID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE mxr_task_id = ?`, in.TaskID).Scan(&existingID)
		if err == nil {
			m, err := s.messageByIDTx(ctx, tx, existingID)
			if err != nil {
				return Message{}, false, err
			}
			if err := tx.Commit(); err != nil {
				return Message{}, false, fmt.Errorf("commit duplicate lookup: %w", err)
			}
			return m, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Message{}, false, fmt.Errorf("lookup task: %w", err)
		}
	}
	if in.DedupeRaw && in.RawSHA256 != "" {
		var existingID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE account_id = ? AND raw_sha256 = ? ORDER BY id LIMIT 1`,
			in.AccountID, in.RawSHA256).Scan(&existingID)
		if err == nil {
			m, err := s.messageByIDTx(ctx, tx, existingID)
			if err != nil {
				return Message{}, false, err
			}
			if err := tx.Commit(); err != nil {
				return Message{}, false, fmt.Errorf("commit raw dedupe: %w", err)
			}
			return m, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Message{}, false, fmt.Errorf("lookup raw message: %w", err)
		}
	}

	var uidNext uint32
	var highest uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT uid_next, highest_modseq FROM mailboxes WHERE id = ? AND account_id = ?`,
		in.MailboxID, in.AccountID).Scan(&uidNext, &highest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, false, ErrMailboxNotFound
		}
		return Message{}, false, fmt.Errorf("read mailbox counters: %w", err)
	}
	uid := uidNext
	modseq := highest + 1
	if _, err := tx.ExecContext(ctx, `UPDATE mailboxes SET uid_next = ?, highest_modseq = ? WHERE id = ?`,
		uidNext+1, modseq, in.MailboxID); err != nil {
		return Message{}, false, fmt.Errorf("advance mailbox counters: %w", err)
	}

	blobID, err := ensureBlob(ctx, tx, in.Raw, in.RawSHA256, in.Size, in.Kind, in.S3Key, in.StoreData || in.Kind == "local")
	if err != nil {
		return Message{}, false, err
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages (
			account_id, mailbox_id, uid, modseq, blob_id, size, internal_date,
			message_id, subject, from_json, to_json, cc_json, bcc_json, reply_to_json,
			in_reply_to_json, references_json, sent_at, headers_json, mime_json,
			raw_sha256, s3_key, mxr_task_id, mxr_correlation_id, is_spam, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.AccountID, in.MailboxID, uid, modseq, blobID, in.Size, in.InternalDate.Unix(),
		in.MessageID, in.Subject, mustJSON(in.From), mustJSON(in.To), mustJSON(in.Cc), mustJSON(in.Bcc),
		mustJSON(in.ReplyTo), mustJSON(in.InReplyTo), mustJSON(in.References), nullableTime(in.SentAt),
		mustJSON(in.Headers), in.MIMETree, in.RawSHA256, in.S3Key, nullIfEmpty(in.TaskID),
		nullIfEmpty(in.CorrelationID), in.IsSpam, now.Unix())
	if err != nil {
		if isUnique(err) {
			return Message{}, false, fmt.Errorf("%w: message", ErrDuplicate)
		}
		return Message{}, false, fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Message{}, false, fmt.Errorf("message id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_flags (message_id, flags) VALUES (?, ?)`, id, uint32(in.Flags)); err != nil {
		return Message{}, false, fmt.Errorf("insert message flags: %w", err)
	}
	for _, keyword := range in.Keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_keywords (message_id, keyword) VALUES (?, ?)`, id, keyword); err != nil {
			return Message{}, false, fmt.Errorf("insert keyword: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO search_content (message_id, subject, sender, recipients, headers, body, attachments)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.Search.Subject, in.Search.Sender, in.Search.Recipients, in.Search.Headers, in.Search.Body, in.Search.Attachments); err != nil {
		return Message{}, false, fmt.Errorf("insert search content: %w", err)
	}
	m, err := s.messageByIDTx(ctx, tx, id)
	if err != nil {
		return Message{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, false, fmt.Errorf("commit insert message: %w", err)
	}
	return m, true, nil
}

func ensureBlob(ctx context.Context, tx *sql.Tx, raw []byte, sha string, size int64, kind, ref string, storeData bool) (int64, error) {
	var id int64
	var existingKind, existingRef string
	err := tx.QueryRowContext(ctx, `SELECT id, kind, ref FROM blobs WHERE sha256 = ?`, sha).Scan(&id, &existingKind, &existingRef)
	if errors.Is(err, sql.ErrNoRows) {
		if kind == "local" && raw == nil {
			return 0, errors.New("store: local blob requires bytes")
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO blobs (sha256, size, kind, ref, refcount, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
			sha, size, kind, ref, time.Now().UTC().Unix())
		if err != nil {
			return 0, fmt.Errorf("insert blob: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("blob id: %w", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("lookup blob: %w", err)
	} else {
		if kind == "s3" && ref != "" && existingKind != "s3" {
			if _, err := tx.ExecContext(ctx, `UPDATE blobs SET kind = 's3', ref = ? WHERE id = ?`, ref, id); err != nil {
				return 0, fmt.Errorf("mark blob s3: %w", err)
			}
		}
	}
	if raw != nil && (storeData || kind == "local") {
		var hasData int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blob_data WHERE blob_id = ?`, id).Scan(&hasData); err != nil {
			return 0, fmt.Errorf("check blob data: %w", err)
		}
		if hasData == 0 {
			compressed, err := compress(raw)
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO blob_data (blob_id, encoding, data) VALUES (?, 'zstd', ?)`, id, compressed); err != nil {
				return 0, fmt.Errorf("insert blob data: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, id); err != nil {
		return 0, fmt.Errorf("increment blob refcount: %w", err)
	}
	return id, nil
}

func compress(raw []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(raw, nil), nil
}

func decompress(data []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("create zstd decoder: %w", err)
	}
	defer dec.Close()
	out, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("decompress blob: %w", err)
	}
	return out, nil
}

// RawBlob returns the raw message bytes for a message. It prefers a local
// copy and returns ErrBlobUnavailable when only a remote reference exists.
