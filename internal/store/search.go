package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MessageState is the lightweight per-message state used for SELECT and IDLE
// polling.
type MessageState struct {
	UID      uint32
	Flags    Flags
	Keywords []string
}

// MailboxState returns the UID, flags, and keywords for every message in a
// mailbox, ordered by UID.
func (s *Store) MailboxState(ctx context.Context, mailboxID int64) ([]MessageState, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT m.uid, COALESCE(f.flags,0),
		       COALESCE((SELECT json_group_array(keyword) FROM message_keywords k WHERE k.message_id = m.id), '[]')
		FROM messages m LEFT JOIN message_flags f ON f.message_id = m.id
		WHERE m.mailbox_id = ? ORDER BY m.uid`, mailboxID)
	if err != nil {
		return nil, fmt.Errorf("mailbox state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MessageState
	for rows.Next() {
		var state MessageState
		var keywords string
		if err := rows.Scan(&state.UID, &state.Flags, &keywords); err != nil {
			return nil, fmt.Errorf("scan mailbox state: %w", err)
		}
		if err := unmarshalJSON(keywords, &state.Keywords); err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

// MailboxUIDs returns all UIDs in ascending order.
func (s *Store) MailboxUIDs(ctx context.Context, mailboxID int64) ([]uint32, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT uid FROM messages WHERE mailbox_id = ? ORDER BY uid`, mailboxID)
	if err != nil {
		return nil, fmt.Errorf("list mailbox uids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var uids []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("scan mailbox uid: %w", err)
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

// MessageCount returns the number of messages in a mailbox.
func (s *Store) MessageCount(ctx context.Context, mailboxID int64) (uint32, error) {
	var count uint32
	if err := s.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE mailbox_id = ?`, mailboxID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return count, nil
}

// SearchContentByIDs loads FTS text for the given message IDs.
func (s *Store) SearchContentByIDs(ctx context.Context, ids []int64) (map[int64]SearchContent, error) {
	out := make(map[int64]SearchContent, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	for _, chunk := range chunks64(ids, 500) {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.reader.QueryContext(ctx, `
			SELECT message_id, subject, sender, recipients, headers, body, attachments
			FROM search_content WHERE message_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("load search content: %w", err)
		}
		for rows.Next() {
			var id int64
			var c SearchContent
			if err := rows.Scan(&id, &c.Subject, &c.Sender, &c.Recipients, &c.Headers, &c.Body, &c.Attachments); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan search content: %w", err)
			}
			out[id] = c
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close search content: %w", err)
		}
	}
	return out, nil
}

// FTSCandidates returns message IDs matching term. It is a candidate prefilter,
// not a substitute for IMAP's substring semantics; callers must verify the
// full criteria against SearchContent.
func (s *Store) FTSCandidates(ctx context.Context, mailboxID int64, term string, limit int) ([]int64, error) {
	query := FTSQuery(term)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	rows, err := s.reader.QueryContext(ctx, `
		SELECT si.rowid
		FROM search_index si
		JOIN messages m ON m.id = si.rowid
		WHERE search_index MATCH ? AND m.mailbox_id = ?
		ORDER BY si.rowid LIMIT ?`, query, mailboxID, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan fts id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FTSQuery converts free text into a conservative FTS5 prefix query. Each
// token is quoted, and tokens are ANDed. It returns "" when no token is at
// least three characters, because short terms are poor FTS candidates.
func FTSQuery(text string) string {
	fields := strings.Fields(text)
	var tokens []string
	for _, field := range fields {
		field = strings.TrimSpace(strings.Trim(field, `"`))
		if len([]rune(field)) < 3 {
			continue
		}
		field = strings.ReplaceAll(field, `"`, `""`)
		tokens = append(tokens, `"`+field+`"*`)
	}
	return strings.Join(tokens, " AND ")
}

// CountIndexed returns the number of rows in the FTS index. It is used by
// doctor to verify that the FTS5 module is available.
func (s *Store) CountIndexed(ctx context.Context) (int, error) {
	var count int
	if err := s.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_index`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count FTS index: %w", err)
	}
	return count, nil
}

// SyncValue reads a value from sync_state.
func (s *Store) SyncValue(ctx context.Context, key string) (string, error) {
	var value string
	err := s.reader.QueryRowContext(ctx, `SELECT value FROM sync_state WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read sync state: %w", err)
	}
	return value, nil
}

// SetSyncValue writes a value to sync_state.
func (s *Store) SetSyncValue(ctx context.Context, key, value string) error {
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO sync_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write sync state: %w", err)
	}
	return nil
}

// DeadLetter stores a message that could not be routed to an account.
func (s *Store) DeadLetter(ctx context.Context, address, taskID, reason string, raw []byte) error {
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO dead_letter (address, task_id, reason, raw, created_at) VALUES (?, ?, ?, ?, ?)`,
		address, taskID, reason, raw, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("store dead letter: %w", err)
	}
	return nil
}

// CountDeadLetters returns the number of stored dead letters.
func (s *Store) CountDeadLetters(ctx context.Context) (int64, error) {
	var count int64
	if err := s.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM dead_letter`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count dead letters: %w", err)
	}
	return count, nil
}
