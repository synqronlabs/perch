package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

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

// ErrBlobUnavailable reports that the message body exists only in remote
// storage and the caller must provide a blob store.
var ErrBlobUnavailable = errors.New("store: blob body is not local")

// EvictLocalData removes a message's local raw copy after an S3 object has
// been confirmed. The blob row and S3 reference remain.

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

// MessageByUID returns one message.

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

// CopyResult reports a COPY or MOVE.
