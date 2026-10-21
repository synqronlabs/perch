// Package store owns perch's SQLite schema, blobs, messages, and search index.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the persistent SQLite state. Reads use a small connection pool;
// writes use a single connection so transactions are serialized without
// SQLITE_BUSY upgrade races.
type Store struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

// Open opens or creates the database at path, applies migrations, and returns
// a ready store. The caller must call Close.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	writer, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := writer.PingContext(ctx); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := writer.ExecContext(ctx, `PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("configure sqlite auto_vacuum: %w", err)
	}
	if err := migrate(ctx, writer); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("secure database permissions: %w", err)
	}

	reader, err := sql.Open("sqlite", readDSN(path))
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	reader.SetMaxOpenConns(8)
	reader.SetMaxIdleConns(8)
	reader.SetConnMaxLifetime(0)
	if err := reader.PingContext(ctx); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, fmt.Errorf("ping sqlite reader: %w", err)
	}
	return &Store{writer: writer, reader: reader, path: path}, nil
}

func dsn(path string) string {
	params := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-65536)",
		"_pragma=wal_autocheckpoint(1000)",
	}
	return filepath.ToSlash(path) + "?" + strings.Join(params, "&")
}

func readDSN(path string) string {
	params := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-65536)",
	}
	return filepath.ToSlash(path) + "?" + strings.Join(params, "&")
}

// Close checkpoints the WAL and closes both pools.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, checkpointErr := s.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	writerErr := s.writer.Close()
	readerErr := s.reader.Close()
	return errors.Join(checkpointErr, writerErr, readerErr)
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// IntegrityCheck runs SQLite's quick_check. It returns nil when the database
// reports ok.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.reader.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("quick_check: %s", result)
	}
	return nil
}

// Checkpoint flushes the WAL into the main database.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	return nil
}

// Backup writes a consistent copy of the database to path using SQLite's
// VACUUM INTO.
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup: destination %q already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := s.writer.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("backup database: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

var migrations = []string{
	`
CREATE TABLE accounts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    primary_address TEXT NOT NULL COLLATE NOCASE UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    password_hash   TEXT NOT NULL,
    disabled        INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);

CREATE TABLE addresses (
    address    TEXT NOT NULL COLLATE NOCASE PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('primary','alias','catchall')),
    created_at INTEGER NOT NULL
);
CREATE INDEX addresses_account_idx ON addresses(account_id);

CREATE TABLE mailboxes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name            TEXT NOT NULL COLLATE NOCASE,
    uid_validity    INTEGER NOT NULL,
    uid_next        INTEGER NOT NULL DEFAULT 1,
    highest_modseq  INTEGER NOT NULL DEFAULT 1,
    subscribed      INTEGER NOT NULL DEFAULT 1,
    special_use     TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    UNIQUE(account_id, name)
);
CREATE INDEX mailboxes_account_idx ON mailboxes(account_id);

CREATE TABLE blobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256      TEXT NOT NULL UNIQUE,
    size        INTEGER NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('local','s3')),
    ref         TEXT NOT NULL DEFAULT '',
    refcount    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE TABLE blob_data (
    blob_id  INTEGER PRIMARY KEY REFERENCES blobs(id) ON DELETE CASCADE,
    encoding TEXT NOT NULL,
    data     BLOB NOT NULL
);

CREATE TABLE messages (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id        INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    mailbox_id        INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    uid               INTEGER NOT NULL,
    modseq            INTEGER NOT NULL,
    blob_id           INTEGER NOT NULL REFERENCES blobs(id),
    size              INTEGER NOT NULL,
    internal_date     INTEGER NOT NULL,
    message_id        TEXT NOT NULL DEFAULT '',
    subject           TEXT NOT NULL DEFAULT '',
    from_json         TEXT NOT NULL DEFAULT '[]',
    to_json           TEXT NOT NULL DEFAULT '[]',
    cc_json           TEXT NOT NULL DEFAULT '[]',
    bcc_json          TEXT NOT NULL DEFAULT '[]',
    reply_to_json     TEXT NOT NULL DEFAULT '[]',
    in_reply_to_json  TEXT NOT NULL DEFAULT '[]',
    references_json   TEXT NOT NULL DEFAULT '[]',
    sent_at           INTEGER,
    headers_json      TEXT NOT NULL DEFAULT '[]',
    mime_json         TEXT NOT NULL DEFAULT '',
    raw_sha256        TEXT NOT NULL DEFAULT '',
    s3_key            TEXT NOT NULL DEFAULT '',
    mxr_task_id       TEXT,
    mxr_correlation_id TEXT,
    is_spam           INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    UNIQUE(mailbox_id, uid)
);
CREATE INDEX messages_mailbox_uid_idx ON messages(mailbox_id, uid);
CREATE INDEX messages_mailbox_modseq_idx ON messages(mailbox_id, modseq);
CREATE UNIQUE INDEX messages_task_idx ON messages(mxr_task_id)
    WHERE mxr_task_id IS NOT NULL AND mxr_task_id <> '';
CREATE INDEX messages_account_sha_idx ON messages(account_id, raw_sha256)
    WHERE raw_sha256 <> '';

CREATE TABLE message_flags (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    flags      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE message_keywords (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    keyword    TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (message_id, keyword)
);

CREATE TABLE search_content (
    message_id  INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    subject     TEXT NOT NULL DEFAULT '',
    sender      TEXT NOT NULL DEFAULT '',
    recipients  TEXT NOT NULL DEFAULT '',
    headers     TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL DEFAULT '',
    attachments TEXT NOT NULL DEFAULT ''
);

CREATE VIRTUAL TABLE search_index USING fts5(
    subject, sender, recipients, headers, body, attachments,
    content='search_content',
    content_rowid='message_id',
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER search_ai AFTER INSERT ON search_content BEGIN
    INSERT INTO search_index(rowid, subject, sender, recipients, headers, body, attachments)
    VALUES (new.message_id, new.subject, new.sender, new.recipients, new.headers, new.body, new.attachments);
END;

CREATE TRIGGER search_ad AFTER DELETE ON search_content BEGIN
    INSERT INTO search_index(search_index, rowid, subject, sender, recipients, headers, body, attachments)
    VALUES ('delete', old.message_id, old.subject, old.sender, old.recipients, old.headers, old.body, old.attachments);
END;

CREATE TRIGGER search_au AFTER UPDATE ON search_content BEGIN
    INSERT INTO search_index(search_index, rowid, subject, sender, recipients, headers, body, attachments)
    VALUES ('delete', old.message_id, old.subject, old.sender, old.recipients, old.headers, old.body, old.attachments);
    INSERT INTO search_index(rowid, subject, sender, recipients, headers, body, attachments)
    VALUES (new.message_id, new.subject, new.sender, new.recipients, new.headers, new.body, new.attachments);
END;

CREATE TABLE dead_letter (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    address    TEXT NOT NULL DEFAULT '',
    task_id    TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL,
    raw        BLOB,
    created_at INTEGER NOT NULL
);

CREATE TABLE sync_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`,
	`
CREATE TABLE outbound (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id      INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    message_ref     TEXT NOT NULL DEFAULT '',
    task_id         TEXT NOT NULL DEFAULT '',
    from_address    TEXT NOT NULL DEFAULT '',
    recipients_json TEXT NOT NULL DEFAULT '[]',
    subject         TEXT NOT NULL DEFAULT '',
    size            INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'accepted',
    attempt         INTEGER NOT NULL DEFAULT 0,
    smtp_code       INTEGER NOT NULL DEFAULT 0,
    enhanced_status TEXT NOT NULL DEFAULT '',
    remote_response TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    accepted_at     INTEGER NOT NULL DEFAULT 0,
    occurred_at     INTEGER NOT NULL DEFAULT 0,
    next_retry_at   INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX outbound_message_ref_idx ON outbound(message_ref) WHERE message_ref <> '';
CREATE INDEX outbound_task_idx ON outbound(task_id) WHERE task_id <> '';
CREATE INDEX outbound_account_idx ON outbound(account_id, created_at DESC);
`,
}
