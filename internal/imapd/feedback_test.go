package imapd

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mxfeedback "github.com/synqronlabs/mxraven-go/mail/feedback"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
)

func TestFeedbackTraining(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/feedback/learn/spam" {
			t.Errorf("path = %q", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "key" || pass != "secret" {
			t.Errorf("basic auth = %q/%q/%t", user, pass, ok)
		}
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"learned","disposition":"spam","matched_hash_kind":"accepted_eml_sha256"}`))
	}))
	defer server.Close()
	client, err := mxfeedback.New(
		mxfeedback.WithBaseURL(server.URL),
		mxfeedback.WithCredentials("key", "secret"),
	)
	if err != nil {
		t.Fatal(err)
	}

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
	raw := []byte("From: sender@example.net\r\nSubject: Spam\r\n\r\nspam body\r\n")
	msg, _, err := st.InsertMessage(context.Background(), store.MessageInput{
		AccountID: account.ID, MailboxID: inbox.ID, Raw: raw,
		Size: int64(len(raw)), InternalDate: time.Now(), Subject: "Spam",
		Search: store.SearchContent{Subject: "Spam", Body: "spam body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, st, hub.New(), nil, slog.Default())
	srv.feedback = client
	srv.trainFeedback(context.Background(), mxfeedback.DispositionSpam, msg)
	select {
	case body := <-received:
		if !strings.Contains(body, "spam body") {
			t.Fatalf("feedback body = %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("feedback was not submitted")
	}
}
