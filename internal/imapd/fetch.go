package imapd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	mxfeedback "github.com/synqronlabs/mxraven-go/mail/feedback"
	"github.com/synqronlabs/perch/internal/mailparse"
	"github.com/synqronlabs/perch/internal/store"
)

func (s *session) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	if err := s.requireAuth(); err != nil {
		return nil, err
	}
	limit := s.srv.cfg.Limits.MaxMessageBytes
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, imapError(imap.StatusResponseTypeNo, imap.ResponseCodeServerBug, "read message literal")
	}
	if int64(len(raw)) > limit {
		return nil, imapError(imap.StatusResponseTypeNo, imap.ResponseCodeTooBig, "message exceeds append limit")
	}
	m, err := s.srv.store.MailboxByName(context.Background(), s.account.ID, mailbox)
	if err != nil {
		return nil, mailboxError(err)
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return nil, imapError(imap.StatusResponseTypeNo, imap.ResponseCodeParse, "parse message")
	}
	var flags store.Flags
	var keywords []string
	internal := time.Now().UTC()
	if options != nil {
		flags, keywords = parseFlags(options.Flags)
		if !options.Time.IsZero() {
			internal = options.Time.UTC()
		}
	}
	msg, _, err := s.srv.store.InsertMessage(context.Background(), store.MessageInput{
		AccountID: s.account.ID, MailboxID: m.ID,
		Raw: raw, StoreData: true, Kind: "local",
		Size: int64(len(raw)), InternalDate: internal,
		MessageID: parsed.MessageID, Subject: parsed.Subject,
		From: convertAddresses(parsed.From), To: convertAddresses(parsed.To),
		Cc: convertAddresses(parsed.Cc), Bcc: convertAddresses(parsed.Bcc),
		ReplyTo:   convertAddresses(parsed.ReplyTo),
		InReplyTo: parsed.InReplyTo, References: parsed.References, SentAt: optionalTime(parsed.Date),
		Headers: convertHeaders(parsed.Headers), MIMETree: parsed.MIMETree,
		Flags: flags, Keywords: keywords,
		Search: store.SearchContent{
			Subject: parsed.Search.Subject, Sender: parsed.Search.Sender,
			Recipients: parsed.Search.Recipients, Headers: parsed.Search.Headers,
			Body: parsed.Search.Body, Attachments: parsed.Search.Attachments,
		},
		DedupeRaw: false,
	})
	if err != nil {
		return nil, imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
	}
	if s.sel != nil && s.sel.mailbox.ID == m.ID {
		s.sel.view = append(s.sel.view, msg.UID)
		s.sel.flags[msg.UID] = msg.Flags
		s.sel.keywords[msg.UID] = msg.Keywords
	}
	s.srv.hub.Notify(m.ID)
	return &imap.AppendData{UID: imap.UID(msg.UID), UIDValidity: m.UIDValidity}, nil
}

func (s *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	sel, err := s.requireSelected()
	if err != nil {
		return err
	}
	uids := s.resolveUIDs(numSet)
	if len(uids) == 0 {
		return nil
	}
	messages, err := s.srv.store.MessagesByUIDs(context.Background(), sel.mailbox.ID, uids)
	if err != nil {
		return err
	}
	byUID := make(map[uint32]store.Message, len(messages))
	for _, msg := range messages {
		byUID[msg.UID] = msg
	}
	markSeen := false
	for _, section := range options.BodySection {
		if !section.Peek {
			markSeen = true
			break
		}
	}
	if markSeen {
		if err := s.markSeen(uids); err != nil {
			return err
		}
	}
	for _, uid := range uids {
		msg, ok := byUID[uid]
		if !ok {
			continue
		}
		seq := s.seqForUID(uid)
		if seq == 0 {
			continue
		}
		writer := w.CreateMessage(seq)
		if err := s.writeFetch(writer, msg, options); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) writeFetch(writer *imapserver.FetchResponseWriter, msg store.Message, options *imap.FetchOptions) error {
	if options.UID {
		writer.WriteUID(imap.UID(msg.UID))
	}
	if options.Flags {
		writer.WriteFlags(s.flagsFor(msg.UID))
	}
	if options.InternalDate {
		writer.WriteInternalDate(msg.InternalDate)
	}
	if options.RFC822Size {
		writer.WriteRFC822Size(msg.Size)
	}
	if options.Envelope {
		writer.WriteEnvelope(envelopeFor(msg))
	}
	if options.BodyStructure != nil {
		tree, err := mailparse.DecodeMIMETree(msg.MIMETree, options.BodyStructure.Extended)
		if err != nil {
			return err
		}
		writer.WriteBodyStructure(tree)
	}
	if len(options.BodySection) > 0 {
		raw, err := s.srv.rawMessage(context.Background(), msg)
		if err != nil {
			return err
		}
		for _, section := range options.BodySection {
			data := imapserver.ExtractBodySection(bytes.NewReader(raw), section)
			data = applyPartial(data, section.Partial)
			out := writer.WriteBodySection(section, int64(len(data)))
			if out == nil {
				continue
			}
			if _, err := out.Write(data); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) markSeen(uids []uint32) error {
	sel := s.sel
	if sel == nil {
		return nil
	}
	unseen := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		if !sel.flags[uid].Has(store.FlagSeen) {
			unseen = append(unseen, uid)
		}
	}
	if len(unseen) == 0 {
		return nil
	}
	updated, err := s.srv.store.StoreFlags(context.Background(), sel.mailbox.ID, unseen, store.FlagsAdd, store.FlagSeen, nil)
	if err != nil {
		return err
	}
	for _, msg := range updated {
		sel.flags[msg.UID] = msg.Flags
		sel.keywords[msg.UID] = msg.Keywords
	}
	s.srv.hub.Notify(sel.mailbox.ID)
	return nil
}

func applyPartial(data []byte, partial *imap.SectionPartial) []byte {
	if partial == nil {
		return data
	}
	offset := partial.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= int64(len(data)) {
		return nil
	}
	end := int64(len(data))
	if partial.Size >= 0 && offset+partial.Size < end {
		end = offset + partial.Size
	}
	return data[offset:end]
}

func (s *session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, _ *imap.StoreOptions) error {
	sel, err := s.requireSelected()
	if err != nil {
		return err
	}
	if sel.readOnly {
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeNoPerm, "mailbox is read-only")
	}
	uids := s.resolveUIDs(numSet)
	if len(uids) == 0 {
		return nil
	}
	system, keywords := parseFlags(flags.Flags)
	op := store.FlagsSet
	switch flags.Op {
	case imap.StoreFlagsAdd:
		op = store.FlagsAdd
	case imap.StoreFlagsDel:
		op = store.FlagsDel
	}
	updated, err := s.srv.store.StoreFlags(context.Background(), sel.mailbox.ID, uids, op, system, keywords)
	if err != nil {
		return err
	}
	for _, msg := range updated {
		sel.flags[msg.UID] = msg.Flags
		sel.keywords[msg.UID] = msg.Keywords
	}
	s.srv.hub.Notify(sel.mailbox.ID)
	if flags.Op != imap.StoreFlagsDel {
		disposition := feedbackDisposition(flags.Flags)
		if disposition != "" {
			for _, message := range updated {
				s.srv.trainFeedback(context.Background(), disposition, message)
			}
		}
	}
	if flags.Silent {
		return nil
	}
	return s.Fetch(w, imap.UIDSetNum(uidsToIMAP(uids)...), &imap.FetchOptions{Flags: true, UID: true})
}

func (s *session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	sel, err := s.requireSelected()
	if err != nil {
		return nil, err
	}
	uids := s.resolveUIDs(numSet)
	if len(uids) == 0 {
		return &imap.CopyData{}, nil
	}
	destMailbox, err := s.srv.store.MailboxByName(context.Background(), s.account.ID, dest)
	if err != nil {
		return nil, mailboxError(err)
	}
	result, err := s.srv.store.CopyMessages(context.Background(), sel.mailbox.ID, destMailbox.ID, uids)
	if err != nil {
		return nil, imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
	}
	s.srv.hub.Notify(destMailbox.ID)
	return copyData(result), nil
}

func (s *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	sel, err := s.requireSelected()
	if err != nil {
		return err
	}
	if sel.readOnly {
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeNoPerm, "mailbox is read-only")
	}
	uids := s.resolveUIDs(numSet)
	if len(uids) == 0 {
		return nil
	}
	destMailbox, err := s.srv.store.MailboxByName(context.Background(), s.account.ID, dest)
	if err != nil {
		return mailboxError(err)
	}
	result, err := s.srv.store.MoveMessages(context.Background(), sel.mailbox.ID, destMailbox.ID, uids)
	if err != nil {
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
	}
	if err := w.WriteCopyData(copyData(result)); err != nil {
		return err
	}
	// Write expunges from highest sequence to lowest.
	seqs := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		if seq := s.seqForUID(uid); seq > 0 {
			seqs = append(seqs, seq)
		}
	}
	sortDescending(seqs)
	for _, seq := range seqs {
		if err := w.WriteExpunge(seq); err != nil {
			return err
		}
	}
	for _, uid := range uids {
		s.removeUID(uid)
	}
	s.srv.hub.Notify(destMailbox.ID)
	return nil
}

func (s *session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	sel, err := s.requireSelected()
	if err != nil {
		return err
	}
	if sel.readOnly {
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeNoPerm, "mailbox is read-only")
	}
	var requested []uint32
	if uids != nil {
		requested = s.resolveUIDs(*uids)
		if len(requested) == 0 {
			return nil
		}
	}
	deleted, err := s.srv.store.Expunge(context.Background(), sel.mailbox.ID, requested)
	if err != nil {
		return err
	}
	seqs := make([]uint32, 0, len(deleted))
	for _, uid := range deleted {
		if seq := s.seqForUID(uid); seq > 0 {
			seqs = append(seqs, seq)
		}
	}
	sortDescending(seqs)
	for _, seq := range seqs {
		if err := w.WriteExpunge(seq); err != nil {
			return err
		}
	}
	for _, uid := range deleted {
		s.removeUID(uid)
	}
	s.srv.hub.Notify(sel.mailbox.ID)
	return nil
}

func copyData(result store.CopyResult) *imap.CopyData {
	return &imap.CopyData{
		UIDValidity: result.UIDValidity,
		SourceUIDs:  uidSet(result.SourceUIDs),
		DestUIDs:    uidSet(result.DestUIDs),
	}
}

func (s *session) removeUID(uid uint32) {
	if s.sel == nil {
		return
	}
	for i, candidate := range s.sel.view {
		if candidate == uid {
			s.sel.view = append(s.sel.view[:i], s.sel.view[i+1:]...)
			break
		}
	}
	delete(s.sel.flags, uid)
	delete(s.sel.keywords, uid)
}

func sortDescending(values []uint32) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] > values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func feedbackDisposition(flags []imap.Flag) mxfeedback.Disposition {
	var spam, ham bool
	for _, flag := range flags {
		switch strings.ToLower(string(flag)) {
		case "$junk":
			spam = true
		case "$notjunk":
			ham = true
		}
	}
	switch {
	case spam && !ham:
		return mxfeedback.DispositionSpam
	case ham && !spam:
		return mxfeedback.DispositionHam
	default:
		return ""
	}
}

func parseFlags(flags []imap.Flag) (store.Flags, []string) {
	var system store.Flags
	var keywords []string
	for _, flag := range flags {
		switch strings.ToLower(string(flag)) {
		case `\seen`:
			system |= store.FlagSeen
		case `\answered`:
			system |= store.FlagAnswered
		case `\flagged`:
			system |= store.FlagFlagged
		case `\deleted`:
			system |= store.FlagDeleted
		case `\draft`:
			system |= store.FlagDraft
		case `\recent`, `\*`:
			// Recent is session state we intentionally do not persist.
		default:
			if !strings.HasPrefix(string(flag), `\`) {
				keywords = append(keywords, string(flag))
			}
		}
	}
	return system, keywords
}

func envelopeFor(msg store.Message) *imap.Envelope {
	date := msg.InternalDate
	if msg.SentAt != nil {
		date = *msg.SentAt
	}
	return &imap.Envelope{
		Date: date, Subject: msg.Subject,
		From: imapAddresses(msg.From), ReplyTo: imapAddresses(msg.ReplyTo),
		To: imapAddresses(msg.To), Cc: imapAddresses(msg.Cc), Bcc: imapAddresses(msg.Bcc),
		InReplyTo: msg.InReplyTo, MessageID: msg.MessageID,
	}
}

func imapAddresses(in []store.Address) []imap.Address {
	out := make([]imap.Address, 0, len(in))
	for _, address := range in {
		at := strings.LastIndexByte(address.Address, '@')
		if at < 0 {
			out = append(out, imap.Address{Name: address.Name, Mailbox: address.Address})
			continue
		}
		out = append(out, imap.Address{Name: address.Name, Mailbox: address.Address[:at], Host: address.Address[at+1:]})
	}
	return out
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

func uidsToIMAP(uids []uint32) []imap.UID {
	out := make([]imap.UID, 0, len(uids))
	for _, uid := range uids {
		out = append(out, imap.UID(uid))
	}
	return out
}
