package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Flags is the system flag bitmask stored for a message.
type Flags uint32

const (
	FlagSeen Flags = 1 << iota
	FlagAnswered
	FlagFlagged
	FlagDeleted
	FlagDraft
)

// Has reports whether all bits in other are set.
func (f Flags) Has(other Flags) bool { return f&other == other }

// Message is a stored message with its metadata, flags, and keywords.
type Message struct {
	ID            int64
	AccountID     int64
	MailboxID     int64
	UID           uint32
	ModSeq        uint64
	BlobID        int64
	Size          int64
	InternalDate  time.Time
	MessageID     string
	Subject       string
	From          []Address
	To            []Address
	Cc            []Address
	Bcc           []Address
	ReplyTo       []Address
	InReplyTo     []string
	References    []string
	SentAt        *time.Time
	Headers       []Header
	MIMETree      string
	RawSHA256     string
	S3Key         string
	TaskID        string
	CorrelationID string
	IsSpam        bool
	Flags         Flags
	Keywords      []string
}

// Blob is the raw-message location shared by one or more messages.
type Blob struct {
	ID       int64
	SHA256   string
	Size     int64
	Kind     string
	Ref      string
	RefCount int64
}

// MessageInput describes a new message. Raw may be nil when the message is
// already in S3 and only metadata is being indexed.
type MessageInput struct {
	AccountID     int64
	MailboxID     int64
	Raw           []byte
	RawSHA256     string
	Kind          string // local or s3; inferred from Raw/S3Key when empty
	S3Key         string
	StoreData     bool // keep a local copy of Raw even for an S3 blob
	Size          int64
	InternalDate  time.Time
	MessageID     string
	Subject       string
	From          []Address
	To            []Address
	Cc            []Address
	Bcc           []Address
	ReplyTo       []Address
	InReplyTo     []string
	References    []string
	SentAt        *time.Time
	Headers       []Header
	MIMETree      string
	TaskID        string
	CorrelationID string
	IsSpam        bool
	Flags         Flags
	Keywords      []string
	Search        SearchContent
	// DedupeRaw rejects an insert when the same account already has a message
	// with this raw SHA-256. Ingest and S3 reconciliation set it; APPEND does
	// not, so a client can intentionally append the same message twice.
	DedupeRaw bool
}

// SearchContent is the text indexed for FTS.
type SearchContent struct {
	Subject     string
	Sender      string
	Recipients  string
	Headers     string
	Body        string
	Attachments string
}

const messageColumns = `
	m.id, m.account_id, m.mailbox_id, m.uid, m.modseq, m.blob_id, m.size, m.internal_date,
	m.message_id, m.subject, m.from_json, m.to_json, m.cc_json, m.bcc_json, m.reply_to_json,
	m.in_reply_to_json, m.references_json, m.sent_at, m.headers_json, m.mime_json,
	m.raw_sha256, m.s3_key, COALESCE(m.mxr_task_id,''), COALESCE(m.mxr_correlation_id,''),
	m.is_spam, COALESCE(f.flags,0),
	COALESCE((SELECT json_group_array(keyword) FROM message_keywords k WHERE k.message_id = m.id), '[]')`

const messageFrom = `
	FROM messages m LEFT JOIN message_flags f ON f.message_id = m.id`

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
func (s *Store) RawBlob(ctx context.Context, messageID int64) ([]byte, Blob, error) {
	var blobID int64
	var blob Blob
	var encoding string
	var data []byte
	err := s.reader.QueryRowContext(ctx, `
		SELECT b.id, b.sha256, b.size, b.kind, b.ref, b.refcount
		FROM blobs b JOIN messages m ON m.blob_id = b.id WHERE m.id = ?`, messageID).
		Scan(&blob.ID, &blob.SHA256, &blob.Size, &blob.Kind, &blob.Ref, &blob.RefCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Blob{}, ErrNotFound
	}
	if err != nil {
		return nil, Blob{}, fmt.Errorf("read blob: %w", err)
	}
	blobID = blob.ID
	err = s.reader.QueryRowContext(ctx, `SELECT encoding, data FROM blob_data WHERE blob_id = ?`, blobID).Scan(&encoding, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, blob, ErrBlobUnavailable
	}
	if err != nil {
		return nil, Blob{}, fmt.Errorf("read blob data: %w", err)
	}
	if encoding != "zstd" {
		return nil, Blob{}, fmt.Errorf("store: unsupported blob encoding %q", encoding)
	}
	raw, err := decompress(data)
	if err != nil {
		return nil, Blob{}, err
	}
	return raw, blob, nil
}

// ErrBlobUnavailable reports that the message body exists only in remote
// storage and the caller must provide a blob store.
var ErrBlobUnavailable = errors.New("store: blob body is not local")

// EvictLocalData removes a message's local raw copy after an S3 object has
// been confirmed. The blob row and S3 reference remain.
func (s *Store) EvictLocalData(ctx context.Context, messageID int64) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin evict local data: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var blobID int64
	if err := tx.QueryRowContext(ctx, `SELECT blob_id FROM messages WHERE id = ?`, messageID).Scan(&blobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lookup message blob: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blob_data WHERE blob_id = ?`, blobID); err != nil {
		return fmt.Errorf("evict blob data: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit evict local data: %w", err)
	}
	return nil
}

// BlobInfo returns the blob metadata for a message.
func (s *Store) BlobInfo(ctx context.Context, messageID int64) (Blob, error) {
	var b Blob
	err := s.reader.QueryRowContext(ctx, `
		SELECT b.id, b.sha256, b.size, b.kind, b.ref, b.refcount
		FROM blobs b JOIN messages m ON m.blob_id = b.id WHERE m.id = ?`, messageID).
		Scan(&b.ID, &b.SHA256, &b.Size, &b.Kind, &b.Ref, &b.RefCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Blob{}, ErrNotFound
	}
	return b, err
}

// MessageByUID returns one message.
func (s *Store) MessageByUID(ctx context.Context, mailboxID int64, uid uint32) (Message, error) {
	return s.scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.mailbox_id = ? AND m.uid = ?`, mailboxID, uid))
}

// MessageByID returns one message.
func (s *Store) MessageByID(ctx context.Context, id int64) (Message, error) {
	return s.scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+` WHERE m.id = ?`, id))
}

// MessageByTaskID returns one message by mxRaven task identifier.
func (s *Store) MessageByTaskID(ctx context.Context, taskID string) (Message, error) {
	return s.scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.mxr_task_id = ? OR m.mxr_correlation_id = ? ORDER BY m.id LIMIT 1`, taskID, taskID))
}

// MessageByRawSHA returns the first message for an account with the given raw
// SHA-256.
func (s *Store) MessageByRawSHA(ctx context.Context, accountID int64, sha string) (Message, error) {
	return s.scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.account_id = ? AND m.raw_sha256 = ? ORDER BY m.id LIMIT 1`, accountID, sha))
}

// MessageByS3Key returns the message bound to an S3 object key.
func (s *Store) MessageByS3Key(ctx context.Context, key string) (Message, error) {
	return s.scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.s3_key = ? ORDER BY m.id LIMIT 1`, key))
}

// BlobByRef returns a blob by its S3 object reference, including refcount-zero
// tombstones. It returns ErrNotFound when no such object has ever been seen.
func (s *Store) BlobByRef(ctx context.Context, ref string) (Blob, error) {
	var b Blob
	err := s.reader.QueryRowContext(ctx, `
		SELECT id, sha256, size, kind, ref, refcount FROM blobs WHERE ref = ? LIMIT 1`, ref).
		Scan(&b.ID, &b.SHA256, &b.Size, &b.Kind, &b.Ref, &b.RefCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Blob{}, ErrNotFound
	}
	if err != nil {
		return Blob{}, fmt.Errorf("lookup blob ref: %w", err)
	}
	return b, nil
}

// MessagesByUIDs returns messages for the given UIDs, ordered by UID.
func (s *Store) MessagesByUIDs(ctx context.Context, mailboxID int64, uids []uint32) ([]Message, error) {
	var out []Message
	for _, chunk := range chunks32(uids, 500) {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, mailboxID)
		for _, uid := range chunk {
			args = append(args, uid)
		}
		messages, err := s.queryMessages(ctx, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.uid IN (`+placeholders+`) ORDER BY m.uid`, args...)
		if err != nil {
			return nil, err
		}
		out = append(out, messages...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// AllMessages returns every message in a mailbox ordered by UID.
func (s *Store) AllMessages(ctx context.Context, mailboxID int64) ([]Message, error) {
	return s.queryMessages(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.mailbox_id = ? ORDER BY m.uid`, mailboxID)
}

// MessagesByIDs returns messages in a mailbox by database ID, ordered by UID.
func (s *Store) MessagesByIDs(ctx context.Context, mailboxID int64, ids []int64) ([]Message, error) {
	var out []Message
	for _, chunk := range chunks64(ids, 500) {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, mailboxID)
		for _, id := range chunk {
			args = append(args, id)
		}
		messages, err := s.queryMessages(ctx, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.id IN (`+placeholders+`) ORDER BY m.uid`, args...)
		if err != nil {
			return nil, err
		}
		out = append(out, messages...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

func (s *Store) queryMessages(ctx context.Context, query string, args ...any) ([]Message, error) {
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) scanMessage(row rowScanner) (Message, error) {
	return scanMessage(row)
}

func (s *Store) messageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (Message, error) {
	return scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+` WHERE m.id = ?`, id))
}

func scanMessage(row rowScanner) (Message, error) {
	var m Message
	var internal, created int64
	var sent sql.NullInt64
	var fromJSON, toJSON, ccJSON, bccJSON, replyJSON, inReplyJSON, refsJSON, headersJSON, keywordsJSON string
	err := row.Scan(
		&m.ID, &m.AccountID, &m.MailboxID, &m.UID, &m.ModSeq, &m.BlobID, &m.Size, &internal,
		&m.MessageID, &m.Subject, &fromJSON, &toJSON, &ccJSON, &bccJSON, &replyJSON,
		&inReplyJSON, &refsJSON, &sent, &headersJSON, &m.MIMETree,
		&m.RawSHA256, &m.S3Key, &m.TaskID, &m.CorrelationID, &m.IsSpam, &m.Flags, &keywordsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("scan message: %w", err)
	}
	_ = created
	m.InternalDate = time.Unix(internal, 0).UTC()
	if sent.Valid {
		t := time.Unix(sent.Int64, 0).UTC()
		m.SentAt = &t
	}
	if err := unmarshalJSON(fromJSON, &m.From); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(toJSON, &m.To); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(ccJSON, &m.Cc); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(bccJSON, &m.Bcc); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(replyJSON, &m.ReplyTo); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(inReplyJSON, &m.InReplyTo); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(refsJSON, &m.References); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(headersJSON, &m.Headers); err != nil {
		return Message{}, err
	}
	if err := unmarshalJSON(keywordsJSON, &m.Keywords); err != nil {
		return Message{}, err
	}
	return m, nil
}

// SetS3Key binds a message to its mxRaven S3 object. taskID may be either the
// accepted task ID or the S3 attempt's correlation ID.
func (s *Store) SetS3Key(ctx context.Context, taskID, objectKey string) (Message, error) {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(objectKey) == "" {
		return Message{}, errors.New("store: task id and object key are required")
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("begin set s3 key: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var id, blobID int64
	err = tx.QueryRowContext(ctx, `SELECT id, blob_id FROM messages
		WHERE mxr_task_id = ? OR mxr_correlation_id = ? ORDER BY id LIMIT 1`, taskID, taskID).Scan(&id, &blobID)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("lookup s3 message: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET s3_key = ? WHERE id = ?`, objectKey, id); err != nil {
		return Message{}, fmt.Errorf("set message s3 key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET kind = 's3', ref = ? WHERE id = ?`, objectKey, blobID); err != nil {
		return Message{}, fmt.Errorf("set blob s3 ref: %w", err)
	}
	m, err := s.messageByIDTx(ctx, tx, id)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit s3 key: %w", err)
	}
	return m, nil
}

// BindS3ByMessageID binds an existing message to an S3 object key.
func (s *Store) BindS3ByMessageID(ctx context.Context, messageID int64, objectKey string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin bind s3: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var blobID int64
	if err := tx.QueryRowContext(ctx, `SELECT blob_id FROM messages WHERE id = ?`, messageID).Scan(&blobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lookup message blob: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET s3_key = ? WHERE id = ?`, objectKey, messageID); err != nil {
		return fmt.Errorf("bind message s3 key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET kind = 's3', ref = ? WHERE id = ?`, objectKey, blobID); err != nil {
		return fmt.Errorf("bind blob s3 key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bind s3: %w", err)
	}
	return nil
}

// SetS3KeyByMessageID binds a message to an S3 object by database ID.
func (s *Store) SetS3KeyByMessageID(ctx context.Context, messageID int64, objectKey string) error {
	return s.BindS3ByMessageID(ctx, messageID, objectKey)
}

// CopyResult reports a COPY or MOVE.
type CopyResult struct {
	SourceUIDs  []uint32
	DestUIDs    []uint32
	UIDValidity uint32
}

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
	for _, chunk := range chunks32(uids, 500) {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, sourceMailboxID)
		for _, uid := range chunk {
			args = append(args, uid)
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.uid IN (`+placeholders+`) ORDER BY m.uid`, args...)
		if err != nil {
			return CopyResult{}, fmt.Errorf("read source messages: %w", err)
		}
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				_ = rows.Close()
				return CopyResult{}, err
			}
			sources = append(sources, m)
		}
		if err := rows.Close(); err != nil {
			return CopyResult{}, fmt.Errorf("close source messages: %w", err)
		}
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
	updated, err := queryMessagesTx(ctx, tx, mailboxID, uids)
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

func queryMessagesTx(ctx context.Context, tx *sql.Tx, mailboxID int64, uids []uint32) ([]Message, error) {
	var out []Message
	for _, chunk := range chunks32(uids, 500) {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, mailboxID)
		for _, uid := range chunk {
			args = append(args, uid)
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.uid IN (`+placeholders+`) ORDER BY m.uid`, args...)
		if err != nil {
			return nil, fmt.Errorf("query updated messages: %w", err)
		}
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, m)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close updated messages: %w", err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

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
		if len(uids) > 0 && !containsUID(uids, t.uid) {
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

func containsUID(uids []uint32, uid uint32) bool {
	for _, candidate := range uids {
		if candidate == uid {
			return true
		}
	}
	return false
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

// MessageIDsByUIDs is a convenience for flag updates that need sequence data.
func (s *Store) MessageIDsByUIDs(ctx context.Context, mailboxID int64, uids []uint32) (map[uint32]int64, error) {
	out := make(map[uint32]int64, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uids)), ",")
	args := make([]any, 0, len(uids)+1)
	args = append(args, mailboxID)
	for _, uid := range uids {
		args = append(args, uid)
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT uid, id FROM messages WHERE mailbox_id = ? AND uid IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup message ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var uid uint32
		var id int64
		if err := rows.Scan(&uid, &id); err != nil {
			return nil, fmt.Errorf("scan message id: %w", err)
		}
		out[uid] = id
	}
	return out, rows.Err()
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func unmarshalJSON(data string, target any) error {
	if strings.TrimSpace(data) == "" {
		data = "[]"
	}
	if err := json.Unmarshal([]byte(data), target); err != nil {
		return fmt.Errorf("decode message field: %w", err)
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Unix()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func chunks32(values []uint32, size int) [][]uint32 {
	var out [][]uint32
	for len(values) > 0 {
		n := size
		if n > len(values) {
			n = len(values)
		}
		out = append(out, values[:n])
		values = values[n:]
	}
	return out
}

func chunks64(values []int64, size int) [][]int64 {
	var out [][]int64
	for len(values) > 0 {
		n := size
		if n > len(values) {
			n = len(values)
		}
		out = append(out, values[:n])
		values = values[n:]
	}
	return out
}
