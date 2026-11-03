package smtpclient

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/synqronlabs/raven/server"
)

type capture struct {
	from       string
	recipients []string
	headers    []byte
	body       []byte
}

type testBackend struct {
	received chan capture
}

func (b *testBackend) NewSession(*server.Conn) (server.Session, error) {
	return &testSession{backend: b}, nil
}

type testSession struct {
	backend    *testBackend
	from       string
	recipients []string
}

func (s *testSession) Mail(from string, _ *server.MailOptions) error {
	s.from = from
	return nil
}

func (s *testSession) Rcpt(to string, _ *server.RcptOptions) error {
	s.recipients = append(s.recipients, to)
	return nil
}

func (s *testSession) Data(headers server.MessageHeaders, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.backend.received <- capture{
		from:       s.from,
		recipients: append([]string(nil), s.recipients...),
		headers:    append([]byte(nil), headers...),
		body:       data,
	}
	return nil
}

func (*testSession) Reset()        {}
func (*testSession) Logout() error { return nil }

func startTestServer(t *testing.T, cfg server.ServerConfig, backend *testBackend) (addr string, stop func()) {
	t.Helper()
	if cfg.Domain == "" {
		cfg.Domain = "test.example.com"
	}
	srv := server.NewServer(backend, cfg)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(context.Background(), listener) }()
	return listener.Addr().String(), func() { _ = srv.Close() }
}

func TestSend(t *testing.T) {
	backend := &testBackend{received: make(chan capture, 1)}
	addr, stop := startTestServer(t, server.ServerConfig{
		MaxMessageBytes: 1 << 20,
		EnableSMTPUTF8:  true,
	}, backend)
	defer stop()

	client, err := Dial(context.Background(), Options{
		Addr:    addr,
		TLSMode: TLSDisabled,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if !client.HasExtension("8BITMIME") {
		t.Fatal("server did not advertise 8BITMIME")
	}
	raw := []byte("Subject: test\r\n\r\nbody\r\n")
	if _, err := client.Send(Envelope{
		From:       "alice@example.com",
		Recipients: []string{"bob@example.net"},
	}, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-backend.received:
		if received.from != "alice@example.com" {
			t.Fatalf("from = %q", received.from)
		}
		if len(received.recipients) != 1 || received.recipients[0] != "bob@example.net" {
			t.Fatalf("recipients = %v", received.recipients)
		}
		if !bytes.Contains(received.headers, []byte("Subject: test")) {
			t.Fatalf("headers = %q", received.headers)
		}
		if string(received.body) != "body\r\n" {
			t.Fatalf("body = %q", received.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestSendRejectsOversizedMessage(t *testing.T) {
	backend := &testBackend{received: make(chan capture, 1)}
	addr, stop := startTestServer(t, server.ServerConfig{
		MaxMessageBytes: 64,
	}, backend)
	defer stop()

	client, err := Dial(context.Background(), Options{
		Addr:    addr,
		TLSMode: TLSDisabled,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	raw := bytes.Repeat([]byte("x"), 100)
	if _, err := client.Send(Envelope{
		From:       "alice@example.com",
		Recipients: []string{"bob@example.net"},
	}, raw); err == nil {
		t.Fatal("oversized message was accepted")
	}
}

func TestSendRequiresAdvertisedSMTPUTF8(t *testing.T) {
	backend := &testBackend{received: make(chan capture, 1)}
	addr, stop := startTestServer(t, server.ServerConfig{
		MaxMessageBytes: 1 << 20,
		EnableSMTPUTF8:  false,
	}, backend)
	defer stop()

	client, err := Dial(context.Background(), Options{
		Addr:    addr,
		TLSMode: TLSDisabled,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Send(Envelope{
		From:       "alice@example.com",
		Recipients: []string{"bob@example.net"},
		SMTPUTF8:   true,
	}, []byte("Subject: test\r\n\r\nbody\r\n")); err == nil {
		t.Fatal("SMTPUTF8 was accepted without server support")
	}
}
