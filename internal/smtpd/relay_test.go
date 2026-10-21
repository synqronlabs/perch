package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/perch/internal/tlsutil"
	"github.com/synqronlabs/raven/client"
	ravenmail "github.com/synqronlabs/raven/mail"
	"github.com/synqronlabs/raven/sasl"
	"github.com/synqronlabs/raven/server"
)

type fakeBackend struct {
	got chan []byte
}

func (b *fakeBackend) NewSession(*server.Conn) (server.Session, error) {
	return &fakeSession{backend: b}, nil
}

type fakeSession struct {
	backend *fakeBackend
	authed  bool
}

func (s *fakeSession) Mail(string, *server.MailOptions) error { return nil }
func (s *fakeSession) Rcpt(string, *server.RcptOptions) error { return nil }
func (s *fakeSession) Data(headers server.MessageHeaders, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	message := append([]byte{}, headers...)
	message = append(message, []byte("\r\n")...)
	message = append(message, data...)
	s.backend.got <- message
	return nil
}
func (s *fakeSession) Reset()        {}
func (s *fakeSession) Logout() error { return nil }
func (s *fakeSession) AuthMechanisms() []string {
	return []string{"PLAIN"}
}
func (s *fakeSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(creds *sasl.Credentials) error {
		if creds.AuthenticationID == "key" && creds.Password == "secret" {
			s.authed = true
			return nil
		}
		return errors.New("bad credentials")
	}), nil
}

func TestRelayToUpstream(t *testing.T) {
	certPath := filepath.Join(t.TempDir(), "cert.pem")
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := tlsutil.GenerateSelfSigned(certPath, keyPath, "localhost"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", certPath)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstreamCtx, upstreamCancel := context.WithCancel(context.Background())
	defer upstreamCancel()
	backend := &fakeBackend{got: make(chan []byte, 1)}
	upstream := server.NewServer(backend, server.ServerConfig{
		Domain: "fake.mxraven.test", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		AllowInsecureAuth: false, MaxMessageBytes: 1 << 20,
	})
	go func() { _ = upstream.Serve(upstreamCtx, upstreamLn) }()
	t.Cleanup(func() { upstreamCancel(); _ = upstream.Close() })
	_, upstreamPort, _ := net.SplitHostPort(upstreamLn.Addr().String())

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Hostname = "localhost"
	cfg.MXRaVen.SubmissionHost = "127.0.0.1"
	cfg.MXRaVen.SubmissionPort = mustAtoi(t, upstreamPort)
	cfg.MXRaVen.Listeners = []config.ListenerConfig{{
		ID: "submission-listener", Kind: "submission", KeyPrefix: "key", KeySecret: "secret",
	}}
	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localAddr := localLn.Addr().String()
	_ = localLn.Close()
	cfg.SMTP.Addr = localAddr
	cfg.SMTP.TLSAddr = ""

	st, err := store.Open(filepath.Join(cfg.DataDir, "perch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	hash, err := password.Hash("local-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(context.Background(), "user@example.com", "User", hash); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := New(cfg, st, nil)
	go func() { _ = relay.Run(ctx, nil) }()
	waitForListener(t, localAddr)

	host, port, _ := net.SplitHostPort(localAddr)
	dialer := client.NewDialer(host, mustAtoi(t, port))
	dialer.Auth = &client.ClientAuth{Username: "user@example.com", Password: "local-secret"}
	conn, err := dialer.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Quit() }()
	raw := "From: user@example.com\r\nTo: recipient@example.net\r\nSubject: relay test\r\nDate: Thu, 22 Oct 2026 03:00:00 +0000\r\nMessage-ID: <relay@example.com>\r\n\r\nhello from perch\r\n"
	_, err = conn.SendRaw(ravenmail.Envelope{
		From: ravenmail.Path{Mailbox: ravenmail.MailboxAddress{LocalPart: "user", Domain: "example.com"}},
		To: []ravenmail.Recipient{{
			Address: ravenmail.Path{Mailbox: ravenmail.MailboxAddress{LocalPart: "recipient", Domain: "example.net"}},
		}},
	}, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-backend.got:
		if !strings.Contains(string(message), "hello from perch") {
			t.Fatalf("upstream message = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not receive the message")
	}
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener %s did not start", addr)
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}
