// Package mailparse extracts IMAP envelope metadata, a cached MIME tree, and
// full-text search text from raw RFC 5322 messages.
package mailparse

import (
	"bytes"
	"encoding/json"
	"fmt"
	htmlstd "html"
	"mime"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	ravenmime "github.com/synqronlabs/mxraven-go/mail/mime"
	xhtml "golang.org/x/net/html"
)

const maxIndexText = 2 << 20

// Address is a decoded mailbox.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Header is one raw header field.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Result is the parsed representation used for storage and search.
type Result struct {
	Subject     string
	MessageID   string
	From        []Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	ReplyTo     []Address
	InReplyTo   []string
	References  []string
	Date        time.Time
	Headers     []Header
	Text        string
	HTML        string
	Attachments []string
	MIMETree    string
	Search      Search
}

// Search is the text written to the FTS index.
type Search struct {
	Subject     string
	Sender      string
	Recipients  string
	Headers     string
	Body        string
	Attachments string
}

// Parse parses a raw message. It never returns a partially initialized result:
// malformed messages fall back to a minimal header-only parse.
func Parse(raw []byte) (*Result, error) {
	email, err := ravenmime.ParseEmail(raw)
	if err != nil {
		return parseFallback(raw), nil
	}
	result := &Result{
		Subject:    email.Subject,
		MessageID:  email.MessageID,
		From:       convertAddresses(email.From),
		To:         convertAddresses(email.To),
		Cc:         convertAddresses(email.Cc),
		Bcc:        convertAddresses(email.Bcc),
		ReplyTo:    convertAddresses(email.ReplyTo),
		InReplyTo:  messageIDs(headerValue(email.Headers, "In-Reply-To")),
		References: messageIDs(headerValue(email.Headers, "References")),
		Date:       email.Date,
		Text:       email.Text,
		HTML:       email.HTML,
		Headers:    convertHeaders(email.Headers),
	}
	for _, attachment := range email.Attachments {
		if attachment.Filename != "" {
			result.Attachments = append(result.Attachments, attachment.Filename)
		}
	}
	result.MIMETree = buildMIMETree(raw)
	result.Search = buildSearch(result)
	return result, nil
}

func parseFallback(raw []byte) *Result {
	result := &Result{}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		result.Search = buildSearch(result)
		result.MIMETree = buildMIMETree(raw)
		return result
	}
	result.Subject = decodeHeader(msg.Header.Get("Subject"))
	result.MessageID = trimMessageID(msg.Header.Get("Message-Id"))
	result.From = parseAddresses(msg.Header.Get("From"))
	result.To = parseAddresses(msg.Header.Get("To"))
	result.Cc = parseAddresses(msg.Header.Get("Cc"))
	result.Bcc = parseAddresses(msg.Header.Get("Bcc"))
	result.ReplyTo = parseAddresses(msg.Header.Get("Reply-To"))
	result.InReplyTo = messageIDs(msg.Header.Get("In-Reply-To"))
	result.References = messageIDs(msg.Header.Get("References"))
	if date, err := msg.Header.Date(); err == nil {
		result.Date = date
	}
	for name, values := range msg.Header {
		for _, value := range values {
			result.Headers = append(result.Headers, Header{Name: name, Value: value})
		}
	}
	result.MIMETree = buildMIMETree(raw)
	result.Search = buildSearch(result)
	return result
}

func buildSearch(result *Result) Search {
	sender := joinAddresses(result.From)
	recipients := strings.Join([]string{
		joinAddresses(result.To), joinAddresses(result.Cc), joinAddresses(result.Bcc),
	}, " ")
	var headerText strings.Builder
	for _, header := range result.Headers {
		headerText.WriteString(header.Name)
		headerText.WriteString(": ")
		headerText.WriteString(header.Value)
		headerText.WriteByte('\n')
	}
	body := result.Text
	if strings.TrimSpace(body) == "" {
		body = htmlText(result.HTML)
	} else if result.HTML != "" {
		body += "\n" + htmlText(result.HTML)
	}
	return Search{
		Subject:     truncate(result.Subject, maxIndexText),
		Sender:      truncate(sender, maxIndexText),
		Recipients:  truncate(recipients, maxIndexText),
		Headers:     truncate(headerText.String(), maxIndexText),
		Body:        truncate(body, maxIndexText),
		Attachments: truncate(strings.Join(result.Attachments, "\n"), maxIndexText),
	}
}

func convertAddresses(in []ravenmime.Address) []Address {
	out := make([]Address, 0, len(in))
	for _, address := range in {
		out = append(out, Address{Name: address.Name, Address: address.Address})
	}
	return out
}

func convertHeaders(in []ravenmime.Header) []Header {
	out := make([]Header, 0, len(in))
	for _, header := range in {
		out = append(out, Header{Name: header.Name, Value: header.Value})
	}
	return out
}

func parseAddresses(value string) []Address {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	list, err := mail.ParseAddressList(value)
	if err != nil {
		return nil
	}
	out := make([]Address, 0, len(list))
	for _, address := range list {
		out = append(out, Address{Name: address.Name, Address: address.Address})
	}
	return out
}

func joinAddresses(addresses []Address) string {
	var parts []string
	for _, address := range addresses {
		if address.Name != "" {
			parts = append(parts, address.Name+" <"+address.Address+">")
		} else {
			parts = append(parts, address.Address)
		}
	}
	return strings.Join(parts, ", ")
}

func headerValue(headers []ravenmime.Header, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func messageIDs(value string) []string {
	var out []string
	for {
		start := strings.IndexByte(value, '<')
		if start < 0 {
			break
		}
		end := strings.IndexByte(value[start:], '>')
		if end < 0 {
			break
		}
		id := strings.TrimSpace(value[start+1 : start+end])
		if id != "" {
			out = append(out, id)
		}
		value = value[start+end+1:]
	}
	return out
}

func trimMessageID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<")
	value = strings.TrimSuffix(value, ">")
	return value
}

func decodeHeader(value string) string {
	if value == "" {
		return ""
	}
	decoded, err := new(mime.WordDecoder).Decode(value)
	if err != nil {
		return value
	}
	return decoded
}

func htmlText(source string) string {
	if source == "" {
		return ""
	}
	tokenizer := xhtml.NewTokenizer(strings.NewReader(source))
	var builder strings.Builder
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			return builder.String()
		case xhtml.TextToken:
			builder.WriteString(htmlstd.UnescapeString(string(tokenizer.Text())))
			builder.WriteByte(' ')
		}
	}
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

// --- MIME tree cache ---
//
// imap.BodyStructure has unexported methods, so it cannot be JSON-encoded
// directly. This package serialises a structurally identical tree and
// reconstructs the go-imap concrete types on demand.

type mimeTree struct {
	Kind        string            `json:"kind"`
	Type        string            `json:"type,omitempty"`
	Subtype     string            `json:"subtype,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	ID          string            `json:"id,omitempty"`
	Description string            `json:"description,omitempty"`
	Encoding    string            `json:"encoding,omitempty"`
	Size        uint32            `json:"size,omitempty"`
	Lines       int64             `json:"lines,omitempty"`
	Children    []mimeTree        `json:"children,omitempty"`
	Message     *messageRFC822    `json:"message,omitempty"`
	Disposition *disposition      `json:"disposition,omitempty"`
	Language    []string          `json:"language,omitempty"`
	Location    string            `json:"location,omitempty"`
}

type messageRFC822 struct {
	Envelope *envelope `json:"envelope,omitempty"`
	Body     *mimeTree `json:"body,omitempty"`
	NumLines int64     `json:"num_lines,omitempty"`
}

type envelope struct {
	Date      time.Time     `json:"date,omitempty"`
	Subject   string        `json:"subject,omitempty"`
	From      []imapAddress `json:"from,omitempty"`
	Sender    []imapAddress `json:"sender,omitempty"`
	ReplyTo   []imapAddress `json:"reply_to,omitempty"`
	To        []imapAddress `json:"to,omitempty"`
	Cc        []imapAddress `json:"cc,omitempty"`
	Bcc       []imapAddress `json:"bcc,omitempty"`
	InReplyTo []string      `json:"in_reply_to,omitempty"`
	MessageID string        `json:"message_id,omitempty"`
}

type imapAddress struct {
	Name    string `json:"name,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
	Host    string `json:"host,omitempty"`
}

type disposition struct {
	Value  string            `json:"value"`
	Params map[string]string `json:"params,omitempty"`
}

func buildMIMETree(raw []byte) string {
	var bs imap.BodyStructure
	func() {
		defer func() { _ = recover() }()
		bs = imapserver.ExtractBodyStructure(bytes.NewReader(raw))
	}()
	if bs == nil {
		bs = &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "7bit"}
	}
	tree := toTree(bs)
	data, err := json.Marshal(tree)
	if err != nil {
		return ""
	}
	return string(data)
}

func toTree(bs imap.BodyStructure) mimeTree {
	switch part := bs.(type) {
	case *imap.BodyStructureMultiPart:
		tree := mimeTree{Kind: "multi", Subtype: part.Subtype}
		for _, child := range part.Children {
			tree.Children = append(tree.Children, toTree(child))
		}
		if part.Extended != nil {
			tree.Params = part.Extended.Params
			tree.Disposition = fromDisposition(part.Extended.Disposition)
			tree.Language = part.Extended.Language
			tree.Location = part.Extended.Location
		}
		return tree
	case *imap.BodyStructureSinglePart:
		tree := mimeTree{
			Kind: "single", Type: part.Type, Subtype: part.Subtype, Params: part.Params,
			ID: part.ID, Description: part.Description, Encoding: part.Encoding, Size: part.Size,
		}
		if part.Text != nil {
			tree.Lines = part.Text.NumLines
		}
		if part.MessageRFC822 != nil {
			msg := &messageRFC822{NumLines: part.MessageRFC822.NumLines, Envelope: fromEnvelope(part.MessageRFC822.Envelope)}
			if part.MessageRFC822.BodyStructure != nil {
				body := toTree(part.MessageRFC822.BodyStructure)
				msg.Body = &body
			}
			tree.Message = msg
		}
		if part.Extended != nil {
			tree.Disposition = fromDisposition(part.Extended.Disposition)
			tree.Language = part.Extended.Language
			tree.Location = part.Extended.Location
		}
		return tree
	}
	return mimeTree{Kind: "single", Type: "application", Subtype: "octet-stream", Encoding: "7bit"}
}

func fromDisposition(in *imap.BodyStructureDisposition) *disposition {
	if in == nil {
		return nil
	}
	return &disposition{Value: in.Value, Params: in.Params}
}

func fromEnvelope(in *imap.Envelope) *envelope {
	if in == nil {
		return nil
	}
	return &envelope{
		Date: in.Date, Subject: in.Subject,
		From: fromAddresses(in.From), Sender: fromAddresses(in.Sender), ReplyTo: fromAddresses(in.ReplyTo),
		To: fromAddresses(in.To), Cc: fromAddresses(in.Cc), Bcc: fromAddresses(in.Bcc),
		InReplyTo: in.InReplyTo, MessageID: in.MessageID,
	}
}

func fromAddresses(in []imap.Address) []imapAddress {
	out := make([]imapAddress, 0, len(in))
	for _, address := range in {
		out = append(out, imapAddress{Name: address.Name, Mailbox: address.Mailbox, Host: address.Host})
	}
	return out
}

// DecodeMIMETree reconstructs a go-imap BodyStructure from the cached JSON.
func DecodeMIMETree(cached string, extended bool) (imap.BodyStructure, error) {
	if strings.TrimSpace(cached) == "" {
		return &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "7bit"}, nil
	}
	var tree mimeTree
	if err := json.Unmarshal([]byte(cached), &tree); err != nil {
		return nil, fmt.Errorf("decode MIME tree: %w", err)
	}
	return fromTree(tree, extended), nil
}

func fromTree(tree mimeTree, extended bool) imap.BodyStructure {
	if tree.Kind == "multi" {
		part := &imap.BodyStructureMultiPart{Subtype: tree.Subtype}
		for _, child := range tree.Children {
			part.Children = append(part.Children, fromTree(child, extended))
		}
		if extended || tree.Params != nil || tree.Disposition != nil || len(tree.Language) > 0 || tree.Location != "" {
			part.Extended = &imap.BodyStructureMultiPartExt{
				Params: tree.Params, Disposition: toDisposition(tree.Disposition),
				Language: tree.Language, Location: tree.Location,
			}
		}
		return part
	}
	part := &imap.BodyStructureSinglePart{
		Type: tree.Type, Subtype: tree.Subtype, Params: tree.Params, ID: tree.ID,
		Description: tree.Description, Encoding: tree.Encoding, Size: tree.Size,
	}
	if tree.Lines > 0 {
		part.Text = &imap.BodyStructureText{NumLines: tree.Lines}
	}
	if tree.Message != nil {
		part.MessageRFC822 = &imap.BodyStructureMessageRFC822{
			Envelope: toEnvelope(tree.Message.Envelope), NumLines: tree.Message.NumLines,
		}
		if tree.Message.Body != nil {
			part.MessageRFC822.BodyStructure = fromTree(*tree.Message.Body, extended)
		}
	}
	if extended || tree.Disposition != nil || len(tree.Language) > 0 || tree.Location != "" {
		part.Extended = &imap.BodyStructureSinglePartExt{
			Disposition: toDisposition(tree.Disposition), Language: tree.Language, Location: tree.Location,
		}
	}
	return part
}

func toDisposition(in *disposition) *imap.BodyStructureDisposition {
	if in == nil {
		return nil
	}
	return &imap.BodyStructureDisposition{Value: in.Value, Params: in.Params}
}

func toEnvelope(in *envelope) *imap.Envelope {
	if in == nil {
		return nil
	}
	return &imap.Envelope{
		Date: in.Date, Subject: in.Subject,
		From: toAddresses(in.From), Sender: toAddresses(in.Sender), ReplyTo: toAddresses(in.ReplyTo),
		To: toAddresses(in.To), Cc: toAddresses(in.Cc), Bcc: toAddresses(in.Bcc),
		InReplyTo: in.InReplyTo, MessageID: in.MessageID,
	}
}

func toAddresses(in []imapAddress) []imap.Address {
	out := make([]imap.Address, 0, len(in))
	for _, address := range in {
		out = append(out, imap.Address{Name: address.Name, Mailbox: address.Mailbox, Host: address.Host})
	}
	return out
}
