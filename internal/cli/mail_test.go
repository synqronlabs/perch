package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/imapd"
	"github.com/synqronlabs/perch/internal/mailclient"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/perch/internal/tlsutil"
)

const (
	mailTestUser = "user@example.com"
	mailTestPass = "secret"
	mailTestRaw1 = "From: Sender <sender@example.net>\r\n" +
		"To: user@example.com\r\n" +
		"Subject: Hello invoice\r\n" +
		"Date: Thu, 22 Oct 2026 03:00:00 +0000\r\n" +
		"Message-ID: <mail-test-1@example.net>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"This is a searchable invoice message.\r\n"
	mailTestRaw2 = "From: Other <other@example.net>\r\n" +
		"To: user@example.com\r\n" +
		"Subject: Second message\r\n" +
		"Date: Fri, 23 Oct 2026 04:00:00 +0000\r\n" +
		"Message-ID: <mail-test-2@example.net>\r\n" +
		"\r\n" +
		"From the desk of the second sender.\r\n"
)

func startMailTestServer(t *testing.T) (addr string, st *store.Store, stop func()) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Storage.Mode = config.StorageSQLite
	st, err := store.Open(filepath.Join(cfg.DataDir, "perch.db"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := password.Hash(mailTestPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(context.Background(), mailTestUser, "User", hash); err != nil {
		t.Fatal(err)
	}
	backend := imapd.New(cfg, st, hub.New(), nil, slog.Default())
	server := imapserver.New(backend.Options(nil, true))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), st, func() {
		_ = server.Close()
		_ = st.Close()
	}
}

func startMailTLSTestServer(t *testing.T) (addr string, certPath string, stop func()) {
	return startMailSecureTestServer(t, true)
}

func startMailStartTLSTestServer(t *testing.T) (addr string, certPath string, stop func()) {
	return startMailSecureTestServer(t, false)
}

func startMailSecureTestServer(t *testing.T, implicit bool) (addr string, certPath string, stop func()) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Storage.Mode = config.StorageSQLite
	st, err := store.Open(filepath.Join(cfg.DataDir, "perch.db"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := password.Hash(mailTestPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(context.Background(), mailTestUser, "User", hash); err != nil {
		t.Fatal(err)
	}
	certDir := t.TempDir()
	certPath = filepath.Join(certDir, "cert.pem")
	keyPath := filepath.Join(certDir, "key.pem")
	if err := tlsutil.GenerateSelfSigned(certPath, keyPath, "localhost"); err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	backend := imapd.New(cfg, st, hub.New(), nil, slog.Default())
	server := imapserver.New(backend.Options(tlsConfig, false))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		listener = tls.NewListener(listener, tlsConfig)
	}
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), certPath, func() {
		_ = server.Close()
		_ = st.Close()
	}
}

func appendMailTestMessages(t *testing.T, addr string) []imap.UID {
	t.Helper()
	client, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Login(mailTestUser, mailTestPass).Wait(); err != nil {
		t.Fatal(err)
	}
	var uids []imap.UID
	for _, raw := range []string{mailTestRaw1, mailTestRaw2} {
		appendCommand := client.Append("INBOX", int64(len(raw)), nil)
		if _, err := appendCommand.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := appendCommand.Close(); err != nil {
			t.Fatal(err)
		}
		appended, err := appendCommand.Wait()
		if err != nil {
			t.Fatal(err)
		}
		uids = append(uids, appended.UID)
	}
	return uids
}

func mailTestArgs(addr string, args ...string) []string {
	base := []string{
		"--server", addr,
		"--plaintext",
		"--username", mailTestUser,
		"--password", mailTestPass,
	}
	return append(append([]string{"mail"}, args...), base...)
}

func TestMailList(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "list"))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	for _, mailbox := range []string{"INBOX", "Sent", "Junk", "Archive"} {
		if !strings.Contains(out, mailbox) {
			t.Fatalf("output does not contain %q:\n%s", mailbox, out)
		}
	}

	out, errOut = captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "list", "--subscribed"))
	})
	if code != 0 {
		t.Fatalf("subscribed exit code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "INBOX") {
		t.Fatalf("subscribed output = %q", out)
	}
}

func TestMailListJSON(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "list", "--json"))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	var inbox mailboxJSON
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var mailbox mailboxJSON
		if err := json.Unmarshal([]byte(line), &mailbox); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if mailbox.Name == "INBOX" {
			inbox = mailbox
		}
	}
	if inbox.Name != "INBOX" || !inbox.Subscribed {
		t.Fatalf("INBOX = %+v", inbox)
	}
}

func TestMailSearchAndFetch(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()
	uids := appendMailTestMessages(t, addr)

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "search", "--query", "invoice", "--json"))
	})
	if code != 0 {
		t.Fatalf("search exit code = %d, stderr = %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("search output = %q", out)
	}
	var message messageJSONValue
	if err := json.Unmarshal([]byte(lines[0]), &message); err != nil {
		t.Fatal(err)
	}
	if message.UID != uint32(uids[0]) {
		t.Fatalf("uid = %d, want %d", message.UID, uids[0])
	}
	if message.Subject != "Hello invoice" {
		t.Fatalf("subject = %q", message.Subject)
	}
	if message.From[0].Address != "sender@example.net" {
		t.Fatalf("from = %+v", message.From)
	}

	out, errOut = captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "fetch", "--uids", "1"))
	})
	if code != 0 {
		t.Fatalf("fetch exit code = %d, stderr = %s", code, errOut)
	}
	if out != mailTestRaw1 {
		t.Fatalf("fetch output = %q, want %q", out, mailTestRaw1)
	}
}

func TestMailFetchMboxEscapesFromLines(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()
	uids := appendMailTestMessages(t, addr)

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "fetch", "--uids", "1,2", "--format", "mbox"))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	if !strings.HasPrefix(out, "From sender@example.net ") {
		t.Fatalf("mbox missing first From_ line:\n%s", out)
	}
	if strings.Count(out, "\nFrom other@example.net ") != 1 {
		t.Fatalf("mbox missing second From_ line:\n%s", out)
	}
	if !strings.Contains(out, ">From the desk of the second sender.") {
		t.Fatalf("mbox did not escape a From line:\n%s", out)
	}
	if strings.Contains(out, "Subject: Hello invoice") == false || strings.Contains(out, "Subject: Second message") == false {
		t.Fatalf("mbox missing subjects:\n%s", out)
	}
	if len(uids) != 2 {
		t.Fatalf("uids = %v", uids)
	}
}

func TestMailFetchMaildir(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()
	appendMailTestMessages(t, addr)
	dest := filepath.Join(t.TempDir(), "Mail")

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "fetch", "--uids", "1", "--format", "maildir", "--dest", dest))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	entries, err := os.ReadDir(filepath.Join(dest, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("new entries = %d", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dest, "new", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != mailTestRaw1 {
		t.Fatalf("maildir message = %q", raw)
	}
}

func TestMailFetchMaildirMarkSeen(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()
	appendMailTestMessages(t, addr)
	dest := filepath.Join(t.TempDir(), "Mail")

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "fetch", "--uids", "1", "--format", "maildir", "--dest", dest, "--mark-seen"))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	entries, err := os.ReadDir(filepath.Join(dest, "cur"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cur entries = %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ":2,S") {
		t.Fatalf("name = %q", entries[0].Name())
	}
}

func TestMailFetchEMLDirectory(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()
	appendMailTestMessages(t, addr)
	dest := t.TempDir()

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run(mailTestArgs(addr, "fetch", "--uids", "1", "--dest", dest))
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "1.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != mailTestRaw1 {
		t.Fatalf("eml message = %q", raw)
	}
}

func TestMailFetchRequiresSelection(t *testing.T) {
	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{"mail", "fetch", "--server", "127.0.0.1:1", "--plaintext", "--username", mailTestUser, "--password", mailTestPass})
	})
	if code == 0 {
		t.Fatal("fetch without a selection succeeded")
	}
	if !strings.Contains(errOut, "refusing to fetch an entire mailbox") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestMailUsesConfigWhenServerOmitted(t *testing.T) {
	addr, _, stop := startMailTestServer(t)
	defer stop()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.IMAP.Addr = addr
	cfg.IMAP.IMAPSAddr = ""
	configPath := filepath.Join(t.TempDir(), "perch.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "list",
			"--config", configPath,
			"--username", mailTestUser,
			"--password", mailTestPass,
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "INBOX") {
		t.Fatalf("output = %q", out)
	}
}

func TestMailListImplicitTLS(t *testing.T) {
	addr, certPath, stop := startMailTLSTestServer(t)
	defer stop()

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "list",
			"--server", "imaps://" + addr,
			"--ca-file", certPath,
			"--username", mailTestUser,
			"--password", mailTestPass,
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "INBOX") {
		t.Fatalf("output = %q", out)
	}
}

func TestMailListStartTLS(t *testing.T) {
	addr, certPath, stop := startMailStartTLSTestServer(t)
	defer stop()

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "list",
			"--server", "imap://" + addr,
			"--ca-file", certPath,
			"--username", mailTestUser,
			"--password", mailTestPass,
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "INBOX") {
		t.Fatalf("output = %q", out)
	}
}

func TestParseServerAddress(t *testing.T) {
	tests := []struct {
		input      string
		wantMode   mailclient.TLSMode
		wantHost   string
		wantScheme bool
		wantErr    bool
	}{
		{"imaps://mail.example.com", mailclient.TLSImplicit, "mail.example.com", true, false},
		{"imaps://mail.example.com/", mailclient.TLSImplicit, "mail.example.com", true, false},
		{"imap://mail.example.com:143", mailclient.TLSStartTLS, "mail.example.com:143", true, false},
		{"mail.example.com:993", mailclient.TLSImplicit, "mail.example.com:993", false, false},
		{"smtp://mail.example.com", mailclient.TLSImplicit, "", true, true},
		{"imaps://mail.example.com/mail", mailclient.TLSImplicit, "", true, true},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			mode, host, hasScheme, err := parseServerAddress(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseServerAddress(%q) succeeded", test.input)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != test.wantMode || host != test.wantHost || hasScheme != test.wantScheme {
				t.Fatalf("got (%v, %q, %v), want (%v, %q, %v)", mode, host, hasScheme, test.wantMode, test.wantHost, test.wantScheme)
			}
		})
	}
}

func TestNormalizeServerAddress(t *testing.T) {
	tests := []struct {
		host string
		mode mailclient.TLSMode
		want string
	}{
		{"mail.example.com", mailclient.TLSImplicit, "mail.example.com:993"},
		{"mail.example.com", mailclient.TLSStartTLS, "mail.example.com:143"},
		{"mail.example.com", mailclient.TLSDisabled, "mail.example.com:143"},
		{"mail.example.com:1993", mailclient.TLSImplicit, "mail.example.com:1993"},
		{"::1", mailclient.TLSDisabled, "[::1]:143"},
		{"[::1]", mailclient.TLSImplicit, "[::1]:993"},
		{":1993", mailclient.TLSImplicit, "localhost:1993"},
		{"", mailclient.TLSImplicit, "localhost:993"},
	}
	for _, test := range tests {
		if got := normalizeServerAddress(test.host, test.mode); got != test.want {
			t.Fatalf("normalizeServerAddress(%q, %v) = %q, want %q", test.host, test.mode, got, test.want)
		}
	}
}

func TestOrderUIDs(t *testing.T) {
	uids := []imap.UID{3, 1, 2}
	if got := orderUIDs(uids, false, 0); !equalUIDs(got, []imap.UID{1, 2, 3}) {
		t.Fatalf("oldest first = %v", got)
	}
	if got := orderUIDs(uids, true, 0); !equalUIDs(got, []imap.UID{3, 2, 1}) {
		t.Fatalf("newest first = %v", got)
	}
	if got := orderUIDs(uids, true, 2); !equalUIDs(got, []imap.UID{3, 2}) {
		t.Fatalf("limited = %v", got)
	}
}

func equalUIDs(a, b []imap.UID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
