package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "perch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAccountRecipientAndMailboxes(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	account, err := st.CreateAccount(ctx, "User@Example.com", "User", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if account.PrimaryAddress != "user@example.com" {
		t.Fatalf("address = %q", account.PrimaryAddress)
	}
	if err := st.AddAlias(ctx, account.ID, "sales@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddCatchAll(ctx, account.ID, "example.net"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"user@example.com", "USER+tag@example.com", "sales@example.com", "anything@example.net"} {
		got, _, err := st.ResolveRecipient(ctx, address)
		if err != nil {
			t.Fatalf("ResolveRecipient(%q): %v", address, err)
		}
		if got.ID != account.ID {
			t.Fatalf("ResolveRecipient(%q) account = %d", address, got.ID)
		}
	}
	if _, _, err := st.ResolveRecipient(ctx, "missing@other.test"); !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("missing recipient error = %v", err)
	}
	mailboxes, err := st.ListMailboxes(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 6 {
		t.Fatalf("default mailboxes = %d", len(mailboxes))
	}
	if _, err := st.CreateMailbox(ctx, account.ID, "Projects/Perch", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MailboxByName(ctx, account.ID, "projects/perch"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMailbox(ctx, account.ID, "Projects/Perch", ""); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate create error = %v", err)
	}
	if err := st.RenameMailbox(ctx, account.ID, "Projects/Perch", "Work/Perch"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MailboxByName(ctx, account.ID, "Work/Perch"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMailbox(ctx, account.ID, "Work"); !errors.Is(err, ErrMailboxHasChildren) {
		t.Fatalf("delete parent error = %v", err)
	}
}

func TestMessageLifecycleAndSearch(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	account, err := st.CreateAccount(ctx, "user@example.com", "User", "hash")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := st.MailboxByName(ctx, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := st.MailboxByName(ctx, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: sender@example.net\r\nSubject: Invoice 42\r\n\r\nPlease pay the invoice.\r\n")
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	msg, inserted, err := st.InsertMessage(ctx, MessageInput{
		AccountID: account.ID, MailboxID: inbox.ID,
		Raw: raw, RawSHA256: sha, Size: int64(len(raw)), InternalDate: time.Now(),
		Subject: "Invoice 42", MessageID: "invoice-42@example.net",
		From: []Address{{Address: "sender@example.net"}},
		Search: SearchContent{
			Subject: "Invoice 42", Sender: "sender@example.net",
			Headers: "Subject: Invoice 42\n", Body: "Please pay the invoice.",
		},
		TaskID: "task-1", DedupeRaw: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inserted || msg.UID != 1 {
		t.Fatalf("inserted=%t uid=%d", inserted, msg.UID)
	}
	if _, inserted, err := st.InsertMessage(ctx, MessageInput{
		AccountID: account.ID, MailboxID: inbox.ID, Raw: raw, RawSHA256: sha,
		Search: SearchContent{Body: "Please pay the invoice."}, TaskID: "task-2", DedupeRaw: true,
	}); err != nil {
		t.Fatal(err)
	} else if inserted {
		t.Fatal("duplicate raw message was inserted")
	}
	candidates, err := st.FTSCandidates(ctx, inbox.ID, "invoice", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != msg.ID {
		t.Fatalf("FTS candidates = %v", candidates)
	}
	blob, _, err := st.RawBlob(ctx, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != string(raw) {
		t.Fatalf("raw blob mismatch")
	}
	updated, err := st.StoreFlags(ctx, inbox.ID, []uint32{1}, FlagsAdd, FlagSeen|FlagFlagged, []string{"$Junk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || !updated[0].Flags.Has(FlagSeen) || len(updated[0].Keywords) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	copyResult, err := st.CopyMessages(ctx, inbox.ID, archive.ID, []uint32{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(copyResult.DestUIDs) != 1 {
		t.Fatalf("copy result = %+v", copyResult)
	}
	moveResult, err := st.MoveMessages(ctx, inbox.ID, archive.ID, []uint32{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(moveResult.DestUIDs) != 1 {
		t.Fatalf("move result = %+v", moveResult)
	}
	if _, err := st.MessageByUID(ctx, inbox.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("source message after move = %v", err)
	}
	// The copy and move are two distinct archive messages.
	if count, err := st.MessageCount(ctx, archive.ID); err != nil || count != 2 {
		t.Fatalf("archive count = %d, err = %v", count, err)
	}
}
