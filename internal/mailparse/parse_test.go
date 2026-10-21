package mailparse

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

const multipartRaw = "From: Pat Taylor <pat@example.org>\r\n" +
	"To: User <user@example.com>\r\n" +
	"Subject: =?UTF-8?Q?Invoice_=232049?=\r\n" +
	"Date: Thu, 16 Oct 2026 16:01:40 +0000\r\n" +
	"Message-ID: <abc123@example.org>\r\n" +
	"In-Reply-To: <parent@example.org>\r\n" +
	"References: <parent@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=outer\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/alternative; boundary=inner\r\n" +
	"\r\n" +
	"--inner\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"The invoice is attached.\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>The <b>invoice</b> is attached.</p>\r\n" +
	"--inner--\r\n" +
	"--outer\r\n" +
	"Content-Type: application/pdf; name=invoice.pdf\r\n" +
	"Content-Disposition: attachment; filename=invoice.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0xLjQK\r\n" +
	"--outer--\r\n"

func TestParse(t *testing.T) {
	result, err := Parse([]byte(multipartRaw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject != "Invoice #2049" {
		t.Fatalf("Subject = %q", result.Subject)
	}
	if result.MessageID != "abc123@example.org" {
		t.Fatalf("MessageID = %q", result.MessageID)
	}
	if len(result.From) != 1 || result.From[0].Address != "pat@example.org" {
		t.Fatalf("From = %+v", result.From)
	}
	if !strings.Contains(result.Search.Body, "invoice") {
		t.Fatalf("search body = %q", result.Search.Body)
	}
	if len(result.Attachments) != 1 || result.Attachments[0] != "invoice.pdf" {
		t.Fatalf("attachments = %+v", result.Attachments)
	}
	tree, err := DecodeMIMETree(result.MIMETree, true)
	if err != nil {
		t.Fatal(err)
	}
	multi, ok := tree.(*imap.BodyStructureMultiPart)
	if !ok {
		t.Fatalf("tree type = %T", tree)
	}
	if multi.Extended == nil {
		t.Fatal("extended body structure was not populated")
	}
	if len(multi.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(multi.Children))
	}
}

func TestParseMalformedFallsBack(t *testing.T) {
	result, err := Parse([]byte("not a valid message"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Search.Body != "" {
		t.Fatalf("unexpected body %q", result.Search.Body)
	}
}
