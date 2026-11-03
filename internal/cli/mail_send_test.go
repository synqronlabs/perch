package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/synqronlabs/perch/internal/tlsutil"
	"github.com/synqronlabs/raven/sasl"
	"github.com/synqronlabs/raven/server"
)

type sentMail struct {
	from       string
	recipients []string
	headers    []byte
	body       []byte
}

type sendTestBackend struct {
	received chan sentMail
}

func (b *sendTestBackend) NewSession(*server.Conn) (server.Session, error) {
	return &sendTestSession{backend: b}, nil
}

type sendTestSession struct {
	backend    *sendTestBackend
	from       string
	recipients []string
}

func (s *sendTestSession) Mail(from string, _ *server.MailOptions) error {
	s.from = from
	return nil
}

func (s *sendTestSession) Rcpt(to string, _ *server.RcptOptions) error {
	s.recipients = append(s.recipients, to)
	return nil
}

func (s *sendTestSession) Data(headers server.MessageHeaders, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.backend.received <- sentMail{
		from:       s.from,
		recipients: append([]string(nil), s.recipients...),
		headers:    append([]byte(nil), headers...),
		body:       data,
	}
	return nil
}

func (*sendTestSession) Reset()        {}
func (*sendTestSession) Logout() error { return nil }

func (*sendTestSession) AuthMechanisms() []string {
	return []string{"PLAIN", "LOGIN"}
}

func (*sendTestSession) Auth(mechanism string) (sasl.Server, error) {
	verify := func(credentials *sasl.Credentials) error {
		if credentials.AuthenticationID == "alice@example.com" && credentials.Password == "secret" {
			return nil
		}
		return server.ErrAuthFailed
	}
	switch mechanism {
	case "PLAIN":
		return sasl.NewPlainServer(verify), nil
	case "LOGIN":
		return sasl.NewLoginServer(verify), nil
	default:
		return nil, server.ErrAuthFailed
	}
}

func startSendTestServer(t *testing.T) (addr string, received <-chan sentMail, stop func()) {
	t.Helper()
	backend := &sendTestBackend{received: make(chan sentMail, 1)}
	srv := server.NewServer(backend, server.ServerConfig{
		Domain:            "test.example.com",
		MaxMessageBytes:   1 << 20,
		EnableSMTPUTF8:    true,
		AllowInsecureAuth: true,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background(), listener) }()
	return listener.Addr().String(), backend.received, func() { _ = srv.Close() }
}

func TestMailSendWithAttachment(t *testing.T) {
	addr, received, stop := startSendTestServer(t)
	defer stop()
	dir := t.TempDir()
	attachmentPath := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(attachmentPath, []byte("attached text\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "send",
			"--smtp-server", addr,
			"--smtp-plaintext",
			"--from", "alice@example.com",
			"--to", "Bob <bob@example.net>",
			"--subject", "Report",
			"--body", "See attached.",
			"--attach", attachmentPath,
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}

	select {
	case mail := <-received:
		if mail.from != "alice@example.com" {
			t.Fatalf("from = %q", mail.from)
		}
		if len(mail.recipients) != 1 || mail.recipients[0] != "bob@example.net" {
			t.Fatalf("recipients = %v", mail.recipients)
		}
		if !bytes.Contains(mail.headers, []byte("Subject: Report")) {
			t.Fatalf("headers = %q", mail.headers)
		}
		if !bytes.Contains(mail.headers, []byte("Content-Type: multipart/mixed")) {
			t.Fatalf("headers = %q", mail.headers)
		}
		if !bytes.Contains(mail.body, []byte("note.txt")) {
			t.Fatalf("body = %q", mail.body)
		}
		if !bytes.Contains(mail.body, []byte("See attached.")) {
			t.Fatalf("body = %q", mail.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestMailCapabilities(t *testing.T) {
	addr, _, stop := startSendTestServer(t)
	defer stop()

	var code int
	out, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "caps",
			"--smtp-server", addr,
			"--smtp-plaintext",
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	for _, capability := range []string{"8BITMIME", "SMTPUTF8", "PIPELINING", "SIZE"} {
		if !strings.Contains(out, capability) {
			t.Fatalf("output does not contain %s:\n%s", capability, out)
		}
	}
}

func TestMailSendAuthenticated(t *testing.T) {
	addr, received, stop := startSendTestServer(t)
	defer stop()

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "send",
			"--smtp-server", addr,
			"--smtp-plaintext",
			"--username", "alice@example.com",
			"--password", "secret",
			"--to", "bob@example.net",
			"--subject", "Authenticated",
			"--body", "Hello.",
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	select {
	case mail := <-received:
		if mail.from != "alice@example.com" {
			t.Fatalf("from = %q", mail.from)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestMailSendImplicitTLS(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
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
	backend := &sendTestBackend{received: make(chan sentMail, 1)}
	srv := server.NewServer(backend, server.ServerConfig{
		Domain:          "test.example.com",
		TLSConfig:       tlsConfig,
		MaxMessageBytes: 1 << 20,
		EnableSMTPUTF8:  true,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener = tls.NewListener(listener, tlsConfig)
	go func() { _ = srv.Serve(context.Background(), listener) }()
	defer func() { _ = srv.Close() }()

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "send",
			"--smtp-server", listener.Addr().String(),
			"--smtp-implicit-tls",
			"--smtp-ca-file", certPath,
			"--from", "alice@example.com",
			"--to", "bob@example.net",
			"--subject", "TLS",
			"--body", "Encrypted.",
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	select {
	case mail := <-backend.received:
		if !bytes.Contains(mail.headers, []byte("Subject: TLS")) {
			t.Fatalf("headers = %q", mail.headers)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestMailSendDryRun(t *testing.T) {
	var code int
	out, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "send",
			"--from", "alice@example.com",
			"--to", "bob@example.net",
			"--subject", "Draft",
			"--body", "Just checking.",
			"--dry-run",
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "Subject: Draft") || !strings.Contains(out, "Just checking.") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, "Message-ID: <") {
		t.Fatalf("output lacks Message-ID: %q", out)
	}
}

func TestMailSendEightBitMIME(t *testing.T) {
	addr, received, stop := startSendTestServer(t)
	defer stop()

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{
			"mail", "send",
			"--smtp-server", addr,
			"--smtp-plaintext",
			"--from", "alice@example.com",
			"--to", "bob@example.net",
			"--subject", "Héllo",
			"--body", "Héllo world",
			"--8bitmime",
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut)
	}
	select {
	case mail := <-received:
		if !bytes.Contains(mail.headers, []byte("Content-Transfer-Encoding: 8bit")) {
			t.Fatalf("headers = %q", mail.headers)
		}
		if !bytes.Contains(mail.body, []byte("Héllo world")) {
			t.Fatalf("body = %q", mail.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}
