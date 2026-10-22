// Package mailmsg adapts parsed messages to the storage input type.
package mailmsg

import (
	"time"

	"github.com/synqronlabs/perch/internal/mailparse"
	"github.com/synqronlabs/perch/internal/store"
)

// Input returns a MessageInput populated from parsed. The caller supplies the
// destination and may override storage-specific fields such as Kind, S3Key,
// TaskID, flags, and deduplication behaviour.
func Input(accountID, mailboxID int64, raw []byte, parsed *mailparse.Result) store.MessageInput {
	return store.MessageInput{
		AccountID: accountID, MailboxID: mailboxID,
		Raw: raw, Size: int64(len(raw)),
		MessageID: parsed.MessageID, Subject: parsed.Subject,
		From: convertAddresses(parsed.From), To: convertAddresses(parsed.To),
		Cc: convertAddresses(parsed.Cc), Bcc: convertAddresses(parsed.Bcc),
		ReplyTo:   convertAddresses(parsed.ReplyTo),
		InReplyTo: parsed.InReplyTo, References: parsed.References, SentAt: optionalTime(parsed.Date),
		Headers: convertHeaders(parsed.Headers), MIMETree: parsed.MIMETree,
		Search: store.SearchContent{
			Subject: parsed.Search.Subject, Sender: parsed.Search.Sender,
			Recipients: parsed.Search.Recipients, Headers: parsed.Search.Headers,
			Body: parsed.Search.Body, Attachments: parsed.Search.Attachments,
		},
	}
}

func convertAddresses(in []mailparse.Address) []store.Address {
	out := make([]store.Address, 0, len(in))
	for _, address := range in {
		out = append(out, store.Address{Name: address.Name, Address: address.Address})
	}
	return out
}

func convertHeaders(in []mailparse.Header) []store.Header {
	out := make([]store.Header, 0, len(in))
	for _, header := range in {
		out = append(out, store.Header{Name: header.Name, Value: header.Value})
	}
	return out
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}
