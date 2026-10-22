package imapd

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/perch/internal/store"
)

func (s *session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if s.sel == nil {
		return nil
	}
	sel := s.sel
	states, err := s.srv.store.MailboxState(context.Background(), sel.mailbox.ID)
	if err != nil {
		return err
	}
	newView, newFlags, newKeywords := mailboxView(states)
	newSet := make(map[uint32]struct{}, len(newView))
	for _, uid := range newView {
		newSet[uid] = struct{}{}
	}

	var expunged []uint32
	for i, uid := range sel.view {
		if _, ok := newSet[uid]; !ok {
			expunged = append(expunged, uint32(i+1))
		}
	}
	if !allowExpunge && len(expunged) > 0 {
		// RFC 3501 forbids EXPUNGE responses during FETCH/STORE/SEARCH.
		// Leave the session view untouched and retry on the next poll.
		return nil
	}
	slices.Sort(expunged)
	slices.Reverse(expunged)
	for _, seq := range expunged {
		if err := w.WriteExpunge(seq); err != nil {
			return err
		}
	}
	countChanged := len(newView) != len(sel.view)
	oldFlags := sel.flags
	oldKeywords := sel.keywords
	sel.view = newView
	sel.flags = newFlags
	sel.keywords = newKeywords
	if countChanged {
		if err := w.WriteNumMessages(uint32(len(newView))); err != nil {
			return err
		}
	}
	for i, uid := range sel.view {
		old, existed := oldFlags[uid]
		if !existed {
			continue
		}
		if old != newFlags[uid] || !sameKeywords(oldKeywords[uid], newKeywords[uid]) {
			if err := w.WriteMessageFlags(uint32(i+1), imap.UID(uid), imapFlags(newFlags[uid], newKeywords[uid])); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameKeywords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, keyword := range a {
		seen[strings.ToLower(keyword)]++
	}
	for _, keyword := range b {
		key := strings.ToLower(keyword)
		if seen[key] == 0 {
			return false
		}
		seen[key]--
	}
	return true
}

func (s *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	if s.sel == nil {
		<-stop
		return nil
	}
	wake := s.sel.wake
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-wake:
			if err := s.Poll(w, true); err != nil {
				return err
			}
		case <-ticker.C:
			if err := s.Poll(w, true); err != nil {
				return err
			}
		}
	}
}

func (s *session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	sel, err := s.requireSelected()
	if err != nil {
		return nil, err
	}
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}

	var messages []store.Message
	term := positiveTerm(criteria)
	if term != "" && store.FTSQuery(term) != "" {
		ids, err := s.srv.store.FTSCandidates(context.Background(), sel.mailbox.ID, term, s.srv.cfg.Limits.SearchMaxMessages)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return &imap.SearchData{All: emptyNumSet(kind)}, nil
		}
		messages, err = s.srv.store.MessagesByIDs(context.Background(), sel.mailbox.ID, ids)
		if err != nil {
			return nil, err
		}
	} else {
		messages, err = s.srv.store.AllMessages(context.Background(), sel.mailbox.ID)
		if err != nil {
			return nil, err
		}
	}
	if len(messages) > s.srv.cfg.Limits.SearchMaxMessages {
		messages = messages[:s.srv.cfg.Limits.SearchMaxMessages]
	}
	ids := make([]int64, 0, len(messages))
	for _, msg := range messages {
		ids = append(ids, msg.ID)
	}
	content, err := s.srv.store.SearchContentByIDs(context.Background(), ids)
	if err != nil {
		return nil, err
	}
	seqByUID := make(map[uint32]uint32, len(sel.view))
	for i, uid := range sel.view {
		seqByUID[uid] = uint32(i + 1)
	}

	var seqs, uids []uint32
	var min, max uint32
	var count uint32
	for _, msg := range messages {
		doc := searchDoc{msg: msg, content: content[msg.ID], seq: seqByUID[msg.UID]}
		if !matchCriteria(doc, criteria) {
			continue
		}
		var number uint32
		if kind == imapserver.NumKindSeq {
			if doc.seq == 0 {
				continue
			}
			seqs = append(seqs, doc.seq)
			number = doc.seq
		} else {
			uids = append(uids, msg.UID)
			number = msg.UID
		}
		if min == 0 || number < min {
			min = number
		}
		if number > max {
			max = number
		}
		count++
	}
	data := &imap.SearchData{Min: min, Max: max, Count: count}
	if kind == imapserver.NumKindSeq {
		data.All = seqSet(seqs)
	} else {
		data.All = uidSet(uids)
	}
	_ = options
	return data, nil
}

type searchDoc struct {
	msg     store.Message
	content store.SearchContent
	seq     uint32
}

func matchCriteria(doc searchDoc, criteria *imap.SearchCriteria) bool {
	if len(criteria.UID) > 0 {
		matched := false
		for _, set := range criteria.UID {
			if setContainsUID(set, doc.msg.UID) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(criteria.SeqNum) > 0 {
		matched := false
		for _, set := range criteria.SeqNum {
			if setContainsSeq(set, doc.seq) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if !criteria.Since.IsZero() && doc.msg.InternalDate.Before(startOfDay(criteria.Since)) {
		return false
	}
	if !criteria.Before.IsZero() && !doc.msg.InternalDate.Before(startOfDay(criteria.Before)) {
		return false
	}
	sent := doc.msg.InternalDate
	if doc.msg.SentAt != nil {
		sent = *doc.msg.SentAt
	}
	if !criteria.SentSince.IsZero() && sent.Before(startOfDay(criteria.SentSince)) {
		return false
	}
	if !criteria.SentBefore.IsZero() && !sent.Before(startOfDay(criteria.SentBefore)) {
		return false
	}
	if criteria.Larger > 0 && doc.msg.Size <= criteria.Larger {
		return false
	}
	if criteria.Smaller > 0 && doc.msg.Size >= criteria.Smaller {
		return false
	}
	for _, flag := range criteria.Flag {
		if !hasFlag(doc, flag) {
			return false
		}
	}
	for _, flag := range criteria.NotFlag {
		if hasFlag(doc, flag) {
			return false
		}
	}
	for _, header := range criteria.Header {
		if !matchHeader(doc, header.Key, header.Value) {
			return false
		}
	}
	for _, text := range criteria.Body {
		if !containsFold(doc.content.Body, text) {
			return false
		}
	}
	for _, text := range criteria.Text {
		if !containsFold(doc.content.Headers+"\n"+doc.content.Body, text) {
			return false
		}
	}
	for i := range criteria.Not {
		if matchCriteria(doc, &criteria.Not[i]) {
			return false
		}
	}
	for _, pair := range criteria.Or {
		if !matchCriteria(doc, &pair[0]) && !matchCriteria(doc, &pair[1]) {
			return false
		}
	}
	return true
}

func matchHeader(doc searchDoc, key, value string) bool {
	var source string
	switch strings.ToLower(key) {
	case "subject":
		source = doc.content.Subject
	case "from":
		source = doc.content.Sender
	case "to":
		source = joinStoreAddresses(doc.msg.To)
	case "cc":
		source = joinStoreAddresses(doc.msg.Cc)
	case "bcc":
		source = joinStoreAddresses(doc.msg.Bcc)
	default:
		for _, header := range doc.msg.Headers {
			if strings.EqualFold(header.Name, key) {
				source += header.Value + "\n"
			}
		}
	}
	if value == "" {
		return source != ""
	}
	return containsFold(source, value)
}

func hasFlag(doc searchDoc, flag imap.Flag) bool {
	if bit, ok := systemFlag(flag); ok {
		return doc.msg.Flags.Has(bit)
	}
	if strings.EqualFold(string(flag), `\recent`) {
		return false
	}
	for _, keyword := range doc.msg.Keywords {
		if strings.EqualFold(keyword, string(flag)) {
			return true
		}
	}
	return false
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func joinStoreAddresses(addresses []store.Address) string {
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address.Name != "" {
			parts = append(parts, address.Name+" <"+address.Address+">")
		} else {
			parts = append(parts, address.Address)
		}
	}
	return strings.Join(parts, ", ")
}

func startOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func setContainsUID(set imap.UIDSet, uid uint32) bool {
	for _, r := range set {
		start, stop := uint32(r.Start), uint32(r.Stop)
		if start == 0 {
			start = 1
		}
		if stop == 0 || uid <= stop {
			if uid >= start {
				return true
			}
		}
	}
	return false
}

func setContainsSeq(set imap.SeqSet, seq uint32) bool {
	for _, r := range set {
		start, stop := r.Start, r.Stop
		if start == 0 {
			start = 1
		}
		if stop == 0 {
			stop = ^uint32(0)
		}
		if seq >= start && seq <= stop {
			return true
		}
	}
	return false
}

func positiveTerm(criteria *imap.SearchCriteria) string {
	var best string
	consider := func(value string) {
		if len(value) > len(best) {
			best = value
		}
	}
	for _, value := range criteria.Text {
		consider(value)
	}
	for _, value := range criteria.Body {
		consider(value)
	}
	for _, header := range criteria.Header {
		consider(header.Value)
	}
	return best
}

func emptyNumSet(kind imapserver.NumKind) imap.NumSet {
	if kind == imapserver.NumKindSeq {
		return imap.SeqSet{}
	}
	return imap.UIDSet{}
}
