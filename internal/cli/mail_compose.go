package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type composeOptions struct {
	From        *mail.Address
	To          []*mail.Address
	Cc          []*mail.Address
	Bcc         []*mail.Address
	ReplyTo     []*mail.Address
	Subject     string
	Text        string
	HTML        string
	HasText     bool
	HasHTML     bool
	Attachments []string
	Headers     []headerField
	InReplyTo   string
	References  []string
	EightBit    bool
	Date        time.Time
	MessageID   string
}

type headerField struct {
	Name  string
	Value string
}

type attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// composeMessage builds a complete RFC 5322 message with CRLF line endings.
// Plain text and HTML bodies are sent as multipart/alternative when both are
// present, and attachments wrap the result in multipart/mixed.
func composeMessage(opts composeOptions) ([]byte, error) {
	if opts.From == nil {
		return nil, errors.New("sender address is required")
	}
	if len(opts.To)+len(opts.Cc)+len(opts.Bcc) == 0 {
		return nil, errors.New("at least one recipient is required")
	}
	if err := validateHeaderValue("Subject", opts.Subject); err != nil {
		return nil, err
	}
	if err := validateHeaderValue("In-Reply-To", opts.InReplyTo); err != nil {
		return nil, err
	}
	for _, reference := range opts.References {
		if err := validateHeaderValue("References", reference); err != nil {
			return nil, err
		}
	}

	date := opts.Date
	if date.IsZero() {
		date = time.Now()
	}
	messageID := opts.MessageID
	if messageID == "" {
		messageID = generateMessageID(opts.From)
	}
	contentType, transferEncoding, body, err := composeBody(opts)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	writeHeader(&out, "From", opts.From.String())
	if len(opts.To) > 0 {
		writeHeader(&out, "To", formatAddressList(opts.To))
	}
	if len(opts.Cc) > 0 {
		writeHeader(&out, "Cc", formatAddressList(opts.Cc))
	}
	writeHeader(&out, "Subject", encodeHeader(opts.Subject))
	writeHeader(&out, "Date", date.Format(time.RFC1123Z))
	writeHeader(&out, "Message-ID", messageID)
	if len(opts.ReplyTo) > 0 {
		writeHeader(&out, "Reply-To", formatAddressList(opts.ReplyTo))
	}
	if opts.InReplyTo != "" {
		writeHeader(&out, "In-Reply-To", formatMessageIDs([]string{opts.InReplyTo}))
	}
	if len(opts.References) > 0 {
		writeHeader(&out, "References", formatMessageIDs(opts.References))
	}
	writeHeader(&out, "MIME-Version", "1.0")
	writeHeader(&out, "Content-Type", contentType)
	if transferEncoding != "" {
		writeHeader(&out, "Content-Transfer-Encoding", transferEncoding)
	}
	for _, header := range opts.Headers {
		if err := validateHeader(header.Name, header.Value); err != nil {
			return nil, err
		}
		writeHeader(&out, header.Name, header.Value)
	}
	out.WriteString("\r\n")
	out.Write(body)
	if !bytes.HasSuffix(out.Bytes(), []byte("\r\n")) {
		out.WriteString("\r\n")
	}
	return out.Bytes(), nil
}

func composeBody(opts composeOptions) (string, string, []byte, error) {
	hasText := opts.HasText
	hasHTML := opts.HasHTML
	if !hasText && !hasHTML {
		hasText = true
	}
	attachments, err := loadAttachments(opts.Attachments)
	if err != nil {
		return "", "", nil, err
	}
	switch {
	case len(attachments) > 0:
		var buf bytes.Buffer
		mixed := multipart.NewWriter(&buf)
		if hasText && hasHTML {
			contentType, alternative, err := renderAlternative(opts.Text, opts.HTML, opts.EightBit)
			if err != nil {
				return "", "", nil, err
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Type", contentType)
			part, err := mixed.CreatePart(header)
			if err != nil {
				return "", "", nil, err
			}
			if _, err := part.Write(alternative); err != nil {
				return "", "", nil, err
			}
		} else {
			contentType := "text/plain; charset=utf-8"
			text := opts.Text
			if !hasText {
				contentType = "text/html; charset=utf-8"
				text = opts.HTML
			}
			if err := writeTextPart(mixed, contentType, text, opts.EightBit); err != nil {
				return "", "", nil, err
			}
		}
		for _, attachment := range attachments {
			if err := writeAttachment(mixed, attachment); err != nil {
				return "", "", nil, err
			}
		}
		if err := mixed.Close(); err != nil {
			return "", "", nil, err
		}
		return "multipart/mixed; boundary=" + mixed.Boundary(), "", buf.Bytes(), nil
	case hasText && hasHTML:
		contentType, body, err := renderAlternative(opts.Text, opts.HTML, opts.EightBit)
		if err != nil {
			return "", "", nil, err
		}
		return contentType, "", body, nil
	default:
		contentType := "text/plain; charset=utf-8"
		text := opts.Text
		if !hasText {
			contentType = "text/html; charset=utf-8"
			text = opts.HTML
		}
		encoding, body, err := encodeTextBody(text, opts.EightBit)
		if err != nil {
			return "", "", nil, err
		}
		return contentType, encoding, body, nil
	}
}

func renderAlternative(text, html string, eightBit bool) (string, []byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writeTextPart(writer, "text/plain; charset=utf-8", text, eightBit); err != nil {
		return "", nil, err
	}
	if err := writeTextPart(writer, "text/html; charset=utf-8", html, eightBit); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return "multipart/alternative; boundary=" + writer.Boundary(), buf.Bytes(), nil
}

func writeTextPart(writer *multipart.Writer, contentType, body string, eightBit bool) error {
	encoding, encoded, err := encodeTextBody(body, eightBit)
	if err != nil {
		return err
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", encoding)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(encoded)
	return err
}

func encodeTextBody(body string, eightBit bool) (string, []byte, error) {
	normalized := normalizeCRLF(body)
	if eightBit {
		return "8bit", []byte(normalized), nil
	}
	var buf bytes.Buffer
	writer := quotedprintable.NewWriter(&buf)
	if _, err := writer.Write([]byte(normalized)); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return "quoted-printable", buf.Bytes(), nil
}

func loadAttachments(paths []string) ([]attachment, error) {
	attachments := make([]attachment, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", path, err)
		}
		filename := filepath.Base(path)
		contentType := mime.TypeByExtension(filepath.Ext(filename))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		attachments = append(attachments, attachment{
			Filename:    filename,
			ContentType: contentType,
			Data:        data,
		})
	}
	return attachments, nil
}

func writeAttachment(writer *multipart.Writer, attachment attachment) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", attachment.ContentType)
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": attachment.Filename,
	}))
	header.Set("Content-Transfer-Encoding", "base64")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	return writeBase64(part, attachment.Data)
}

func writeBase64(w io.Writer, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		if _, err := io.WriteString(w, encoded[:76]+"\r\n"); err != nil {
			return err
		}
		encoded = encoded[76:]
	}
	if encoded != "" {
		if _, err := io.WriteString(w, encoded+"\r\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeHeader(out *bytes.Buffer, name, value string) {
	out.WriteString(name)
	out.WriteString(": ")
	out.WriteString(value)
	out.WriteString("\r\n")
}

func encodeHeader(value string) string {
	if !containsNonASCII(value) {
		return value
	}
	return mime.QEncoding.Encode("utf-8", value)
}

func formatAddressList(addresses []*mail.Address) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		values = append(values, address.String())
	}
	return strings.Join(values, ", ")
}

func formatMessageIDs(values []string) string {
	var ids []string
	for _, value := range values {
		for _, id := range strings.Fields(value) {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if !strings.HasPrefix(id, "<") {
				id = "<" + id
			}
			if !strings.HasSuffix(id, ">") {
				id += ">"
			}
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, " ")
}

func generateMessageID(from *mail.Address) string {
	domain := "localhost"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 && at+1 < len(from.Address) {
		domain = from.Address[at+1:]
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), uuid.NewString(), domain)
}

func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

func containsNonASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return true
		}
	}
	return false
}

func validateHeader(name, value string) error {
	if name == "" {
		return errors.New("header name is empty")
	}
	for _, r := range name {
		if !isHeaderTokenRune(r) {
			return fmt.Errorf("invalid header name %q", name)
		}
	}
	return validateHeaderValue(name, value)
}

func validateHeaderValue(name, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s header must not contain CR or LF", name)
	}
	return nil
}

func isHeaderTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		return true
	default:
		return false
	}
}
