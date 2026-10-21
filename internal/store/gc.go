package store

import (
	"context"
	"fmt"
	"time"
)

// GCLocalOrphans removes local blobs that are no longer referenced by any
// message. When dryRun is true it only reports what would be removed.
func (s *Store) GCLocalOrphans(ctx context.Context, dryRun bool) (int, int64, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin local gc: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT b.id, b.size FROM blobs b
		WHERE b.kind = 'local' AND b.refcount <= 0
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.blob_id = b.id)`)
	if err != nil {
		return 0, 0, fmt.Errorf("list orphan blobs: %w", err)
	}
	var ids []int64
	var count int
	var bytes int64
	for rows.Next() {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("scan orphan blob: %w", err)
		}
		ids = append(ids, id)
		count++
		bytes += size
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("close orphan blobs: %w", err)
	}
	if !dryRun {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, id); err != nil {
				return 0, 0, fmt.Errorf("delete orphan blob: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, 0, fmt.Errorf("commit local gc: %w", err)
		}
	}
	return count, bytes, nil
}

// S3Tombstones returns deleted-message blob rows whose S3 objects still exist.
func (s *Store) S3Tombstones(ctx context.Context) ([]Blob, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT b.id, b.sha256, b.size, b.kind, b.ref, b.refcount FROM blobs b
		WHERE b.kind = 's3' AND b.refcount <= 0 AND b.ref <> ''
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.blob_id = b.id)
		ORDER BY b.id`)
	if err != nil {
		return nil, fmt.Errorf("list s3 tombstones: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Blob
	for rows.Next() {
		var b Blob
		if err := rows.Scan(&b.ID, &b.SHA256, &b.Size, &b.Kind, &b.Ref, &b.RefCount); err != nil {
			return nil, fmt.Errorf("scan s3 tombstone: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBlob removes a blob row by ID.
func (s *Store) DeleteBlob(ctx context.Context, id int64) error {
	if _, err := s.writer.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

// PurgeDeadLetters deletes dead letters older than before.
func (s *Store) PurgeDeadLetters(ctx context.Context, before time.Time) (int, error) {
	res, err := s.writer.ExecContext(ctx, `DELETE FROM dead_letter WHERE created_at < ?`, before.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("purge dead letters: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// IncrementalVacuum releases free pages without a full VACUUM.
func (s *Store) IncrementalVacuum(ctx context.Context) error {
	if _, err := s.writer.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		return fmt.Errorf("incremental vacuum: %w", err)
	}
	return nil
}

// SpoolStats reports the compressed size and count of local spool copies for
// S3-backed messages.
func (s *Store) SpoolStats(ctx context.Context) (int64, int, error) {
	var bytes int64
	var count int
	if err := s.reader.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(length(bd.data)),0), COUNT(*)
		FROM blob_data bd JOIN blobs b ON b.id = bd.blob_id
		WHERE b.kind = 's3'`).Scan(&bytes, &count); err != nil {
		return 0, 0, fmt.Errorf("spool stats: %w", err)
	}
	return bytes, count, nil
}

// EnforceSpoolLimit evicts confirmed S3 spool copies until their compressed
// size is at most maxBytes. Unconfirmed copies are never evicted.
func (s *Store) EnforceSpoolLimit(ctx context.Context, maxBytes int64) (int, int64, error) {
	if maxBytes <= 0 {
		return 0, 0, nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin spool gc: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT bd.blob_id, length(bd.data)
		FROM blob_data bd JOIN blobs b ON b.id = bd.blob_id
		WHERE b.kind = 's3'
		  AND EXISTS (SELECT 1 FROM messages m WHERE m.blob_id = b.id AND m.s3_key <> '')
		ORDER BY b.created_at, b.id`)
	if err != nil {
		return 0, 0, fmt.Errorf("list spool copies: %w", err)
	}
	type item struct {
		id   int64
		size int64
	}
	var items []item
	var total int64
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.size); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("scan spool copy: %w", err)
		}
		items = append(items, it)
		total += it.size
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("close spool copies: %w", err)
	}
	var evicted int
	var freed int64
	for _, it := range items {
		if total <= maxBytes {
			break
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blob_data WHERE blob_id = ?`, it.id); err != nil {
			return 0, 0, fmt.Errorf("evict spool copy: %w", err)
		}
		total -= it.size
		freed += it.size
		evicted++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit spool gc: %w", err)
	}
	return evicted, freed, nil
}

// CountOutbound returns outbound status counts for doctor.
func (s *Store) CountOutbound(ctx context.Context) (map[string]int, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT status, COUNT(*) FROM outbound GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count outbound: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan outbound count: %w", err)
		}
		out[status] = count
	}
	return out, rows.Err()
}
