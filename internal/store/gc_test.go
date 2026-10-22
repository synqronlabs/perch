package store

import (
	"context"
	"testing"
	"time"
)

func TestGCLocalOrphans(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Unix()
	if _, err := st.writer.ExecContext(ctx, `
		INSERT INTO blobs (sha256, size, kind, ref, refcount, created_at)
		VALUES ('orphan', 42, 'local', '', 0, ?),
		       ('live', 7, 'local', '', 1, ?),
		       ('tombstone', 9, 's3', 'perch/live.eml', 0, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}

	count, bytes, err := st.GCLocalOrphans(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || bytes != 42 {
		t.Fatalf("dry run = %d blobs, %d bytes", count, bytes)
	}
	var remaining int
	if err := st.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE sha256 = 'orphan'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("dry run deleted an orphan")
	}

	count, bytes, err = st.GCLocalOrphans(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || bytes != 42 {
		t.Fatalf("gc = %d blobs, %d bytes", count, bytes)
	}
	if err := st.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE sha256 = 'orphan'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("orphan was not deleted")
	}
	var live int
	if err := st.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE sha256 IN ('live','tombstone')`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 2 {
		t.Fatalf("live/tombstone blobs = %d, want 2", live)
	}
}
