package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAddress(t *testing.T, value string) *mail.Address {
	t.Helper()
	address, err := mail.ParseAddress(value)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func TestComposePlainMessage(t *testing.T) {
	raw, err := composeMessage(composeOptions{
		From:      testAddress(t, "Alice <alice@example.com>"),
		To:        []*mail.Address{testAddress(t, "Bob <bob@example.net>")},
		Bcc:       []*mail.Address{testAddress(t, "Secret <secret@example.net>")},
		Subject:   "Hello",
		Text:      "Line one\nLine two",
		HasText:   true,
		Date:      time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		MessageID: "<fixed@example.com>",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := message.Header.Get("From"); !strings.Contains(got, "Alice") || !strings.Contains(got, "alice@example.com") {
		t.Fatalf("From = %q", got)
	}
	if got := message.Header.Get("To"); !strings.Contains(got, "Bob") || !strings.Contains(got, "bob@example.net") {
		t.Fatalf("To = %q", got)
	}
	if got := message.Header.Get("Bcc"); got != "" {
		t.Fatalf("Bcc header = %q", got)
	}
	if got := message.Header.Get("Subject"); got != "Hello" {
		t.Fatalf("Subject = %q", got)
	}
	if got := message.Header.Get("Message-ID"); got != "<fixed@example.com>" {
		t.Fatalf("Message-ID = %q", got)
	}
	contentType := message.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/plain; charset=utf-8") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if got := message.Header.Get("Content-Transfer-Encoding"); got != "quoted-printable" {
		t.Fatalf("Content-Transfer-Encoding = %q", got)
	}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "Line one\r\nLine two\r\n" {
		t.Fatalf("body = %q", decoded)
	}
}

func TestComposeAlternativeWithAttachment(t *testing.T) {
	dir := t.TempDir()
	attachmentPath := filepath.Join(dir, "report.txt")
	attachmentData := []byte("attachment contents\n")
	if err := os.WriteFile(attachmentPath, attachmentData, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := composeMessage(composeOptions{
		From:        testAddress(t, "alice@example.com"),
		To:          []*mail.Address{testAddress(t, "bob@example.net")},
		Subject:     "Report",
		Text:        "Plain body",
		HTML:        "<p>HTML body</p>",
		HasText:     true,
		HasHTML:     true,
		Attachments: []string{attachmentPath},
		Date:        time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		MessageID:   "<fixed@example.com>",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("Content-Type = %q", mediaType)
	}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		t.Fatal(err)
	}
	parts := readMultipart(t, body, params["boundary"])
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	alternativeType, alternativeParams, err := mime.ParseMediaType(parts[0].header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if alternativeType != "multipart/alternative" {
		t.Fatalf("first part type = %q", alternativeType)
	}
	alternativeParts := readMultipart(t, parts[0].body, alternativeParams["boundary"])
	if len(alternativeParts) != 2 {
		t.Fatalf("alternative parts = %d", len(alternativeParts))
	}
	if got := alternativeParts[0].header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("text part = %q", got)
	}
	if got := alternativeParts[1].header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("html part = %q", got)
	}

	if got := parts[1].header.Get("Content-Disposition"); !strings.Contains(got, `filename=report.txt`) {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := parts[1].header.Get("Content-Transfer-Encoding"); got != "base64" {
		t.Fatalf("attachment encoding = %q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(parts[1].body), "\r\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, attachmentData) {
		t.Fatalf("attachment = %q", decoded)
	}
}

func TestComposeEightBitAndEncodedSubject(t *testing.T) {
	raw, err := composeMessage(composeOptions{
		From:      testAddress(t, "alice@example.com"),
		To:        []*mail.Address{testAddress(t, "bob@example.net")},
		Subject:   "Héllo",
		Text:      "Héllo world",
		HasText:   true,
		EightBit:  true,
		Date:      time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		MessageID: "<fixed@example.com>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("Subject: =?utf-8?q?")) {
		t.Fatalf("subject was not encoded:\n%s", raw)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := message.Header.Get("Content-Transfer-Encoding"); got != "8bit" {
		t.Fatalf("Content-Transfer-Encoding = %q", got)
	}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Héllo world") {
		t.Fatalf("body = %q", body)
	}
}

func TestComposeRejectsHeaderInjection(t *testing.T) {
	_, err := composeMessage(composeOptions{
		From:    testAddress(t, "alice@example.com"),
		To:      []*mail.Address{testAddress(t, "bob@example.net")},
		Subject: "hello\r\nBcc: evil@example.net",
		Text:    "body",
		HasText: true,
	})
	if err == nil {
		t.Fatal("header injection was accepted")
	}
	if _, err := parseSendHeaders([]string{"X-Test: ok\r\nBcc: evil@example.net"}); err == nil {
		t.Fatal("extra header injection was accepted")
	}
	if _, err := parseSendHeaders([]string{"From: other@example.net"}); err == nil {
		t.Fatal("reserved header was accepted")
	}
}

func TestRequiresSMTPUTF8(t *testing.T) {
	ascii := testAddress(t, "alice@example.com")
	unicode := testAddress(t, "用户@example.com")
	if requiresSMTPUTF8(ascii, []*mail.Address{ascii}, nil) {
		t.Fatal("ASCII addresses require SMTPUTF8")
	}
	if !requiresSMTPUTF8(ascii, []*mail.Address{unicode}, nil) {
		t.Fatal("non-ASCII recipient did not require SMTPUTF8")
	}
	if !requiresSMTPUTF8(ascii, []*mail.Address{ascii}, []headerField{{Name: "X-Test", Value: "héllo"}}) {
		t.Fatal("non-ASCII header did not require SMTPUTF8")
	}
}

type parsedPart struct {
	header mail.Header
	body   []byte
}

func readMultipart(t *testing.T, body []byte, boundary string) []parsedPart {
	t.Helper()
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var parts []parsedPart
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, parsedPart{
			header: mail.Header(part.Header),
			body:   data,
		})
	}
	return parts
}
