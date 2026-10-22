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

	"github.com/emersion/go-imap/v2"
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

func TestFeedbackKeywordMapping(t *testing.T) {
	tests := []struct {
		name  string
		flags []imap.Flag
		want  mxfeedback.Disposition
	}{
		{"dollar junk", []imap.Flag{"$Junk"}, mxfeedback.DispositionSpam},
		{"legacy junk", []imap.Flag{"Junk"}, mxfeedback.DispositionSpam},
		{"dollar not junk", []imap.Flag{"$NotJunk"}, mxfeedback.DispositionHam},
		{"legacy not junk", []imap.Flag{"NonJunk"}, mxfeedback.DispositionHam},
		{"conflicting", []imap.Flag{"$Junk", "NonJunk"}, ""},
		{"unrelated", []imap.Flag{imap.FlagSeen}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := feedbackDisposition(tt.flags); got != tt.want {
				t.Fatalf("feedbackDisposition(%v) = %q, want %q", tt.flags, got, tt.want)
			}
		})
	}
}

func TestMoveDisposition(t *testing.T) {
	tests := []struct {
		name   string
		source string
		dest   string
		want   mxfeedback.Disposition
	}{
		{"into junk", `\Archive`, `\Junk`, mxfeedback.DispositionSpam},
		{"out of junk", `\Junk`, `\Archive`, mxfeedback.DispositionHam},
		{"junk to inbox", `\Junk`, "", mxfeedback.DispositionHam},
		{"junk to trash", `\Junk`, `\Trash`, ""},
		{"unrelated", `\Archive`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := moveDisposition(store.Mailbox{SpecialUse: tt.source}, store.Mailbox{SpecialUse: tt.dest})
			if got != tt.want {
				t.Fatalf("moveDisposition(%q, %q) = %q, want %q", tt.source, tt.dest, got, tt.want)
			}
		})
	}
}

func TestMoveTrainsFeedback(t *testing.T) {
	type request struct {
		path string
		body string
	}
	requests := make(chan request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- request{path: r.URL.Path, body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		disposition := strings.TrimPrefix(r.URL.Path, "/v1/feedback/learn/")
		_, _ = w.Write([]byte(`{"status":"learned","disposition":"` + disposition + `","matched_hash_kind":"accepted_eml_sha256"}`))
	}))
	defer server.Close()
	client, err := mxfeedback.New(
		mxfeedback.WithBaseURL(server.URL),
		mxfeedback.WithCredentials("key", "secret"),
	)
	if err != nil {
		t.Fatal(err)
	}

	addr, _, backend, stop := startTestServerWithBackend(t)
	defer stop()
	backend.feedback = client

	imapClient := dialTest(t, addr)
	if _, err := imapClient.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendCmd := imapClient.Append("INBOX", int64(len(testRaw)), nil)
	if _, err := appendCmd.Write([]byte(testRaw)); err != nil {
		t.Fatal(err)
	}
	if err := appendCmd.Close(); err != nil {
		t.Fatal(err)
	}
	appended, err := appendCmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	moveData, err := imapClient.Move(imap.UIDSetNum(appended.UID), "Junk").Wait()
	if err != nil {
		t.Fatal(err)
	}
	junkUIDs, ok := moveData.DestUIDs.(imap.UIDSet)
	if !ok {
		t.Fatalf("move into Junk data = %+v", moveData)
	}
	junkNums, ok := junkUIDs.Nums()
	if !ok || len(junkNums) == 0 {
		t.Fatalf("move into Junk UIDs = %v", junkUIDs)
	}

	if _, err := imapClient.Select("Junk", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := imapClient.Move(imap.UIDSetNum(junkNums...), "INBOX").Wait(); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"/v1/feedback/learn/spam": false,
		"/v1/feedback/learn/ham":  false,
	}
	deadline := time.After(5 * time.Second)
	for remaining := len(want); remaining > 0; {
		select {
		case req := <-requests:
			if _, ok := want[req.path]; !ok {
				t.Fatalf("unexpected feedback path %q", req.path)
			}
			if !strings.Contains(req.body, "invoice message") {
				t.Fatalf("feedback body = %q", req.body)
			}
			want[req.path] = true
			remaining--
		case <-deadline:
			t.Fatalf("feedback submissions = %v", want)
		}
	}
	for path, seen := range want {
		if !seen {
			t.Fatalf("no feedback submitted to %s", path)
		}
	}
}
