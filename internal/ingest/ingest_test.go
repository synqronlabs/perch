package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/synqronlabs/mxraven-go/mail/webhook"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
)

const inboundRaw = "From: Sender <sender@example.net>\r\nTo: user@example.com\r\nSubject: Inbound hello\r\nDate: Thu, 22 Oct 2026 03:00:00 +0000\r\nMessage-ID: <inbound@example.net>\r\n\r\nsearchable inbound body\r\n"

func TestHandleAccepted(t *testing.T) {
	raw := []byte(inboundRaw)
	sum := sha256.Sum256(raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing authorization header")
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	st, err := store.Open(filepath.Join(cfg.DataDir, "perch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	hash, err := password.Hash("secret")
	if err != nil {
		t.Fatal(err)
	}
	account, err := st.CreateAccount(context.Background(), "user@example.com", "User", hash)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := st.MailboxByName(context.Background(), account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New()
	wake, unsubscribe := h.Subscribe(inbox.ID)
	defer unsubscribe()
	ingester := New(cfg, st, nil, h, nil)
	err = ingester.handleAccepted(context.Background(), &webhook.InboundEmail{
		TaskID: "task-inbound-1", AcceptedAtUTC: time.Now().Unix(),
		Envelope: webhook.Envelope{RcptTo: []string{"user@example.com"}},
		Message:  webhook.MessageSummary{Subject: "Inbound hello", MessageID: "inbound@example.net"},
		RawEmail: webhook.RawEmail{
			URL: server.URL, AccessToken: "test-token", TokenType: "Bearer",
			SizeBytes: int64(len(raw)), SHA256Hex: hex.EncodeToString(sum[:]),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("hub was not notified")
	}
	count, err := st.MessageCount(context.Background(), inbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("message count = %d", count)
	}
	candidates, err := st.FTSCandidates(context.Background(), inbox.ID, "searchable", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("fts candidates = %v", candidates)
	}
	// A redelivery of the same task must be a no-op.
	if err := ingester.handleAccepted(context.Background(), &webhook.InboundEmail{
		TaskID:   "task-inbound-1",
		Envelope: webhook.Envelope{RcptTo: []string{"user@example.com"}},
		RawEmail: webhook.RawEmail{URL: server.URL, AccessToken: "test-token", SizeBytes: int64(len(raw)), SHA256Hex: hex.EncodeToString(sum[:])},
	}); err != nil {
		t.Fatal(err)
	}
	count, _ = st.MessageCount(context.Background(), inbox.ID)
	if count != 1 {
		t.Fatalf("duplicate task created %d messages", count)
	}
}

func TestMailboxAddressFromKey(t *testing.T) {
	template := "perch/{{.recipient.local}}/{{.recipient.domain}}/{{.task.id}}.eml"
	got, err := mailboxAddressFromKey(template, "perch/User/Example.com/abc-123.eml")
	if err != nil {
		t.Fatal(err)
	}
	if got != "user@example.com" {
		t.Fatalf("address = %q", got)
	}
	if _, err := mailboxAddressFromKey(template, "wrong/user/example.com/abc.eml"); err == nil {
		t.Fatal("mismatched key was accepted")
	}
	legacy := "perch/{{.mailbox.address}}/{{.task.id}}.eml"
	got, err = mailboxAddressFromKey(legacy, "perch/User@Example.com/abc-123.eml")
	if err != nil || got != "user@example.com" {
		t.Fatalf("legacy address = %q, err = %v", got, err)
	}
}
