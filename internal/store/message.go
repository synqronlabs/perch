package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
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

// MessageByUID returns one message.
func (s *Store) MessageByUID(ctx context.Context, mailboxID int64, uid uint32) (Message, error) {
	return scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.mailbox_id = ? AND m.uid = ?`, mailboxID, uid))
}

// MessageByTaskID returns one message by mxRaven task identifier.

// MessageByTaskID returns one message by mxRaven task identifier.
func (s *Store) MessageByTaskID(ctx context.Context, taskID string) (Message, error) {
	return scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.mxr_task_id = ? OR m.mxr_correlation_id = ? ORDER BY m.id LIMIT 1`, taskID, taskID))
}

// MessageByRawSHA returns the first message for an account with the given raw
// SHA-256.

// MessageByRawSHA returns the first message for an account with the given raw
// SHA-256.
func (s *Store) MessageByRawSHA(ctx context.Context, accountID int64, sha string) (Message, error) {
	return scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.account_id = ? AND m.raw_sha256 = ? ORDER BY m.id LIMIT 1`, accountID, sha))
}

// MessageByS3Key returns the message bound to an S3 object key.

// MessageByS3Key returns the message bound to an S3 object key.
func (s *Store) MessageByS3Key(ctx context.Context, key string) (Message, error) {
	return scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+`
		WHERE m.s3_key = ? ORDER BY m.id LIMIT 1`, key))
}

// BlobByRef returns a blob by its S3 object reference, including refcount-zero
// tombstones. It returns ErrNotFound when no such object has ever been seen.

// MessagesByUIDs returns messages for the given UIDs, ordered by UID.
func (s *Store) MessagesByUIDs(ctx context.Context, mailboxID int64, uids []uint32) ([]Message, error) {
	return messagesForUIDs(ctx, s.reader, mailboxID, uids)
}

// AllMessages returns every message in a mailbox ordered by UID.

// AllMessages returns every message in a mailbox ordered by UID.
func (s *Store) AllMessages(ctx context.Context, mailboxID int64) ([]Message, error) {
	return queryMessages(ctx, s.reader, `SELECT `+messageColumns+messageFrom+`
		WHERE m.mailbox_id = ? ORDER BY m.uid`, mailboxID)
}

// MessagesByIDs returns messages in a mailbox by database ID, ordered by UID.

// MessagesByIDs returns messages in a mailbox by database ID, ordered by UID.
func (s *Store) MessagesByIDs(ctx context.Context, mailboxID int64, ids []int64) ([]Message, error) {
	var out []Message
	for _, chunk := range chunks(ids, 500) {
		messages, err := queryMessages(ctx, s.reader, `SELECT `+messageColumns+messageFrom+`
			WHERE m.mailbox_id = ? AND m.id IN (`+placeholders(len(chunk))+`) ORDER BY m.uid`,
			argsWith(mailboxID, chunk)...)
		if err != nil {
			return nil, err
		}
		out = append(out, messages...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryMessages(ctx context.Context, q rowQueryer, query string, args ...any) ([]Message, error) {
	rows, err := q.QueryContext(ctx, query, args...)
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

func (s *Store) messageByIDTx(ctx context.Context, tx *sql.Tx, id int64) (Message, error) {
	return scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+` WHERE m.id = ?`, id))
}

func scanMessage(row rowScanner) (Message, error) {
	var m Message
	var internal int64
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

func chunks[T any](values []T, size int) [][]T {
	var out [][]T
	for len(values) > 0 {
		n := min(size, len(values))
		out = append(out, values[:n])
		values = values[n:]
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func argsWith[T any](first any, values []T) []any {
	args := make([]any, 0, len(values)+1)
	args = append(args, first)
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

func argsFor[T any](values []T) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return args
}
