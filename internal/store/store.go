// Package store owns perch's SQLite schema, blobs, messages, and search index.
package store

import (
	"context"
	"database/sql"
	"embed"
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
	return &Store{writer: writer, reader: reader}, nil
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

//go:embed migrations/*.sql
var migrationFS embed.FS

var migrations = []string{
	mustReadMigration("migrations/001_initial.sql"),
	mustReadMigration("migrations/002_outbound.sql"),
}

func mustReadMigration(name string) string {
	data, err := migrationFS.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(data)
}
