package imapd

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
)

type session struct {
	srv     *Server
	conn    *imapserver.Conn
	account store.Account
	sel     *selectedMailbox
}

type selectedMailbox struct {
	mailbox     store.Mailbox
	readOnly    bool
	view        []uint32
	flags       map[uint32]store.Flags
	keywords    map[uint32][]string
	wake        <-chan struct{}
	unsubscribe func()
}

func (s *session) Close() error {
	s.unselect()
	return nil
}

func (s *session) Login(username, passwordValue string) error {
	key := remoteHost(s.conn) + "|" + strings.ToLower(strings.TrimSpace(username))
	if !s.srv.login.Allowed(key) {
		s.srv.log.Warn("IMAP login throttled", "username", username, "remote", remoteAddr(s.conn))
		return imapserver.ErrAuthFailed
	}
	account, err := s.srv.store.AccountByAddress(context.Background(), username)
	if err != nil {
		s.srv.login.Failure(key)
		s.srv.log.Warn("IMAP login failed", "username", username, "remote", remoteAddr(s.conn), "error", err)
		return imapserver.ErrAuthFailed
	}
	ok, err := password.Verify(account.PasswordHash, passwordValue)
	if err != nil || !ok {
		s.srv.login.Failure(key)
		s.srv.log.Warn("IMAP login failed", "username", username, "remote", remoteAddr(s.conn))
		return imapserver.ErrAuthFailed
	}
	s.srv.login.Success(key)
	s.account = account
	s.srv.log.Info("IMAP login", "account", account.PrimaryAddress, "remote", remoteAddr(s.conn))
	return nil
}

func (s *session) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	if err := s.requireAuth(); err != nil {
		return nil, err
	}
	m, err := s.srv.store.MailboxByName(context.Background(), s.account.ID, mailbox)
	if err != nil {
		return nil, mailboxError(err)
	}
	if s.sel != nil {
		s.unselect()
	}
	sel, err := s.loadSelected(m, options != nil && options.ReadOnly)
	if err != nil {
		return nil, err
	}
	s.sel = sel
	st, err := s.srv.store.Status(context.Background(), m.ID)
	if err != nil {
		return nil, err
	}
	data := &imap.SelectData{
		Flags:          selectFlags(),
		PermanentFlags: permanentFlags(),
		NumMessages:    uint32(len(sel.view)),
		NumRecent:      0,
		UIDNext:        imap.UID(m.UIDNext),
		UIDValidity:    m.UIDValidity,
	}
	for i, uid := range sel.view {
		if !sel.flags[uid].Has(store.FlagSeen) {
			data.FirstUnseenSeqNum = uint32(i + 1)
			break
		}
	}
	if st.Unseen == 0 {
		data.FirstUnseenSeqNum = 0
	}
	return data, nil
}

func (s *session) loadSelected(m store.Mailbox, readOnly bool) (*selectedMailbox, error) {
	states, err := s.srv.store.MailboxState(context.Background(), m.ID)
	if err != nil {
		return nil, err
	}
	sel := &selectedMailbox{
		mailbox: m, readOnly: readOnly,
		view:     make([]uint32, 0, len(states)),
		flags:    make(map[uint32]store.Flags, len(states)),
		keywords: make(map[uint32][]string, len(states)),
	}
	for _, state := range states {
		sel.view = append(sel.view, state.UID)
		sel.flags[state.UID] = state.Flags
		sel.keywords[state.UID] = state.Keywords
	}
	sel.wake, sel.unsubscribe = s.srv.hub.Subscribe(m.ID)
	return sel, nil
}

func (s *session) Unselect() error {
	s.unselect()
	return nil
}

func (s *session) unselect() {
	if s.sel == nil {
		return
	}
	if s.sel.unsubscribe != nil {
		s.sel.unsubscribe()
	}
	s.sel = nil
}

func (s *session) Create(mailbox string, options *imap.CreateOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	special := ""
	if options != nil && len(options.SpecialUse) > 0 {
		special = string(options.SpecialUse[0])
	}
	_, err := s.srv.store.CreateMailbox(context.Background(), s.account.ID, mailbox, special)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeAlreadyExists, "mailbox already exists")
		}
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
	}
	return nil
}

func (s *session) Delete(mailbox string) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	err := s.srv.store.DeleteMailbox(context.Background(), s.account.ID, mailbox)
	if err != nil {
		if errors.Is(err, store.ErrMailboxNotFound) {
			return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeNonExistent, "mailbox does not exist")
		}
		if errors.Is(err, store.ErrMailboxHasChildren) {
			return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeHasChildren, "mailbox has children")
		}
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
	}
	if s.sel != nil && strings.EqualFold(s.sel.mailbox.Name, mailbox) {
		s.unselect()
	}
	return nil
}

func (s *session) Rename(mailbox, newName string, _ *imap.RenameOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	err := s.srv.store.RenameMailbox(context.Background(), s.account.ID, mailbox, newName)
	if err != nil {
		if errors.Is(err, store.ErrMailboxNotFound) {
			return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeNonExistent, "mailbox does not exist")
		}
		if errors.Is(err, store.ErrDuplicate) {
			return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeAlreadyExists, "destination exists")
		}
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
	}
	if s.sel != nil && strings.EqualFold(s.sel.mailbox.Name, mailbox) {
		s.unselect()
	}
	return nil
}

func (s *session) Subscribe(mailbox string) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	if err := s.srv.store.SetSubscribed(context.Background(), s.account.ID, mailbox, true); err != nil {
		return mailboxError(err)
	}
	return nil
}

func (s *session) Unsubscribe(mailbox string) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	if err := s.srv.store.SetSubscribed(context.Background(), s.account.ID, mailbox, false); err != nil {
		return mailboxError(err)
	}
	return nil
}

func (s *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	mailboxes, err := s.srv.store.ListMailboxes(context.Background(), s.account.ID)
	if err != nil {
		return err
	}
	for _, mailbox := range mailboxes {
		if options != nil && options.SelectSubscribed && !mailbox.Subscribed {
			continue
		}
		if options != nil && options.SelectSpecialUse && mailbox.SpecialUse == "" {
			continue
		}
		matched := false
		for _, pattern := range patterns {
			if imapserver.MatchList(mailbox.Name, '/', ref, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		attrs := mailboxAttrs(mailbox, mailboxes)
		data := &imap.ListData{Attrs: attrs, Delim: '/', Mailbox: mailbox.Name}
		if options != nil && options.ReturnSubscribed && mailbox.Subscribed {
			data.Attrs = append(data.Attrs, imap.MailboxAttrSubscribed)
		}
		if options != nil && options.ReturnChildren {
			childSubscribed := false
			for _, other := range mailboxes {
				if strings.HasPrefix(strings.ToLower(other.Name), strings.ToLower(mailbox.Name)+"/") {
					childSubscribed = childSubscribed || other.Subscribed
				}
			}
			data.ChildInfo = &imap.ListDataChildInfo{Subscribed: childSubscribed}
		}
		if options != nil && options.ReturnStatus != nil {
			status, err := s.Status(mailbox.Name, options.ReturnStatus)
			if err != nil {
				return err
			}
			data.Status = status
		}
		if err := w.WriteList(data); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	if err := s.requireAuth(); err != nil {
		return nil, err
	}
	m, err := s.srv.store.MailboxByName(context.Background(), s.account.ID, mailbox)
	if err != nil {
		return nil, mailboxError(err)
	}
	st, err := s.srv.store.Status(context.Background(), m.ID)
	if err != nil {
		return nil, err
	}
	data := &imap.StatusData{Mailbox: m.Name, UIDNext: imap.UID(m.UIDNext), UIDValidity: m.UIDValidity}
	if options == nil {
		return data, nil
	}
	if options.NumMessages {
		value := st.Messages
		data.NumMessages = &value
	}
	if options.NumRecent {
		zero := uint32(0)
		data.NumRecent = &zero
	}
	if options.NumUnseen {
		value := st.Unseen
		data.NumUnseen = &value
	}
	if options.NumDeleted {
		value := st.Deleted
		data.NumDeleted = &value
	}
	if options.Size {
		value := st.Size
		data.Size = &value
	}
	if options.AppendLimit {
		value := s.AppendLimit()
		data.AppendLimit = &value
	}
	return data, nil
}

func (s *session) Namespace() (*imap.NamespaceData, error) {
	if err := s.requireAuth(); err != nil {
		return nil, err
	}
	return &imap.NamespaceData{
		Personal: []imap.NamespaceDescriptor{{Prefix: "", Delim: '/'}},
	}, nil
}

func (s *session) AppendLimit() uint32 {
	return uint32(s.srv.cfg.Limits.MaxMessageBytes)
}

func (s *session) requireAuth() error {
	if s.account.ID == 0 {
		return imapError(imap.StatusResponseTypeBad, imap.ResponseCodeAuthorizationFailed, "authenticate first")
	}
	return nil
}

func (s *session) requireSelected() (*selectedMailbox, error) {
	if err := s.requireAuth(); err != nil {
		return nil, err
	}
	if s.sel == nil {
		return nil, imapError(imap.StatusResponseTypeBad, imap.ResponseCodeClientBug, "select a mailbox first")
	}
	return s.sel, nil
}

func (s *session) seqForUID(uid uint32) uint32 {
	if s.sel == nil {
		return 0
	}
	for i, candidate := range s.sel.view {
		if candidate == uid {
			return uint32(i + 1)
		}
	}
	return 0
}

func (s *session) flagsFor(uid uint32) []imap.Flag {
	return imapFlags(s.sel.flags[uid], s.sel.keywords[uid])
}

func selectFlags() []imap.Flag {
	return []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft}
}

func permanentFlags() []imap.Flag {
	return []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft, imap.FlagWildcard}
}

func imapFlags(flags store.Flags, keywords []string) []imap.Flag {
	out := make([]imap.Flag, 0, 5+len(keywords))
	if flags.Has(store.FlagSeen) {
		out = append(out, imap.FlagSeen)
	}
	if flags.Has(store.FlagAnswered) {
		out = append(out, imap.FlagAnswered)
	}
	if flags.Has(store.FlagFlagged) {
		out = append(out, imap.FlagFlagged)
	}
	if flags.Has(store.FlagDeleted) {
		out = append(out, imap.FlagDeleted)
	}
	if flags.Has(store.FlagDraft) {
		out = append(out, imap.FlagDraft)
	}
	for _, keyword := range keywords {
		out = append(out, imap.Flag(keyword))
	}
	return out
}

func mailboxAttrs(mailbox store.Mailbox, all []store.Mailbox) []imap.MailboxAttr {
	var attrs []imap.MailboxAttr
	hasChildren := false
	prefix := strings.ToLower(mailbox.Name) + "/"
	for _, other := range all {
		if strings.HasPrefix(strings.ToLower(other.Name), prefix) {
			hasChildren = true
			break
		}
	}
	if hasChildren {
		attrs = append(attrs, imap.MailboxAttrHasChildren)
	} else {
		attrs = append(attrs, imap.MailboxAttrHasNoChildren)
	}
	if mailbox.SpecialUse != "" {
		attrs = append(attrs, imap.MailboxAttr(mailbox.SpecialUse))
	}
	return attrs
}

func mailboxError(err error) error {
	if errors.Is(err, store.ErrMailboxNotFound) || errors.Is(err, store.ErrNotFound) {
		return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeNonExistent, "mailbox does not exist")
	}
	return imapError(imap.StatusResponseTypeNo, imap.ResponseCodeCannot, err.Error())
}

func imapError(kind imap.StatusResponseType, code imap.ResponseCode, text string) error {
	return &imap.Error{Type: kind, Code: code, Text: text}
}

func remoteAddr(conn *imapserver.Conn) string {
	if conn == nil || conn.NetConn() == nil {
		return ""
	}
	return conn.NetConn().RemoteAddr().String()
}

func remoteHost(conn *imapserver.Conn) string {
	addr := remoteAddr(conn)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// resolveUIDs expands a NumSet against the selected mailbox view. Sequence
// numbers are converted to UIDs; UID sets are returned as-is.
func (s *session) resolveUIDs(set imap.NumSet) []uint32 {
	if s.sel == nil || set == nil {
		return nil
	}
	switch value := set.(type) {
	case imap.SeqSet:
		var out []uint32
		for _, r := range value {
			start, stop := r.Start, r.Stop
			if start == 0 {
				start = 1
			}
			if stop == 0 || stop > uint32(len(s.sel.view)) {
				stop = uint32(len(s.sel.view))
			}
			for n := start; n <= stop && n >= start; n++ {
				out = append(out, s.sel.view[n-1])
				if n == ^uint32(0) {
					break
				}
			}
		}
		return uniqueUIDs(out)
	case imap.UIDSet:
		var max uint32
		if len(s.sel.view) > 0 {
			max = s.sel.view[len(s.sel.view)-1]
		}
		var out []uint32
		for _, r := range value {
			start, stop := uint32(r.Start), uint32(r.Stop)
			if start == 0 {
				start = 1
			}
			if stop == 0 || stop > max {
				stop = max
			}
			for n := start; n <= stop && n >= start; n++ {
				out = append(out, n)
				if n == ^uint32(0) {
					break
				}
			}
		}
		return uniqueUIDs(out)
	default:
		return nil
	}
}

func uniqueUIDs(in []uint32) []uint32 {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i] < in[j] })
	out := in[:1]
	for _, uid := range in[1:] {
		if uid != out[len(out)-1] {
			out = append(out, uid)
		}
	}
	return out
}

func uidSet(uids []uint32) imap.UIDSet {
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	return set
}

func seqSet(nums []uint32) imap.SeqSet {
	var set imap.SeqSet
	for _, num := range nums {
		set.AddNum(num)
	}
	return set
}
