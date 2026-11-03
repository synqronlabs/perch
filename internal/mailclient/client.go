// Package mailclient implements the small IMAP client used by the perch CLI.
// It deliberately exposes only the operations needed to list, search, and
// fetch mail so that the command-line surface stays predictable.
package mailclient

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// TLSMode selects how the client secures its IMAP connection.
type TLSMode int

const (
	// TLSImplicit connects with TLS immediately (usually port 993).
	TLSImplicit TLSMode = iota
	// TLSStartTLS connects in plaintext and upgrades with STARTTLS.
	TLSStartTLS
	// TLSDisabled sends everything in plaintext. It is only appropriate for
	// trusted loopback connections.
	TLSDisabled
)

// Options configures Dial.
type Options struct {
	Addr      string
	TLSMode   TLSMode
	TLSConfig *tls.Config
	Username  string
	Password  string
	Timeout   time.Duration
}

// Client wraps the go-imap client with the operations perch exposes.
type Client struct {
	conn *imapclient.Client
}

// Dial connects and authenticates to an IMAP server.
func Dial(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.Addr) == "" {
		return nil, errors.New("IMAP server address is required")
	}
	if opts.Username == "" {
		return nil, errors.New("IMAP username is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	imapOptions := &imapclient.Options{
		TLSConfig: opts.TLSConfig,
		Dialer:    &net.Dialer{Timeout: timeout},
	}

	var conn *imapclient.Client
	var err error
	switch opts.TLSMode {
	case TLSImplicit:
		conn, err = imapclient.DialTLS(opts.Addr, imapOptions)
	case TLSStartTLS:
		conn, err = imapclient.DialStartTLS(opts.Addr, imapOptions)
	case TLSDisabled:
		conn, err = imapclient.DialInsecure(opts.Addr, imapOptions)
	default:
		return nil, fmt.Errorf("unknown TLS mode %d", opts.TLSMode)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", opts.Addr, err)
	}
	if err := conn.WaitGreeting(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("greeting from %s: %w", opts.Addr, err)
	}
	if err := conn.Login(opts.Username, opts.Password).Wait(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("login as %s: %w", opts.Username, err)
	}
	return &Client{conn: conn}, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_ = c.conn.Logout().Wait()
	return c.conn.Close()
}

// Mailbox is one entry returned by LIST.
type Mailbox struct {
	Name       string
	Delimiter  string
	Attributes []string
	SpecialUse []string
	Subscribed bool
}

// Mailboxes lists mailboxes. When subscribedOnly is set, only subscribed
// mailboxes are returned.
func (c *Client) Mailboxes(subscribedOnly bool) ([]Mailbox, error) {
	caps := c.conn.Caps()
	if subscribedOnly && !supportsListExtended(caps) {
		return nil, errors.New("server does not support LIST-EXTENDED; cannot filter by subscription")
	}
	options := &imap.ListOptions{}
	usedExtensions := false
	if supportsListExtended(caps) {
		options.ReturnSubscribed = true
		if subscribedOnly {
			options.SelectSubscribed = true
		}
		usedExtensions = true
	}
	if supportsSpecialUse(caps) {
		options.ReturnSpecialUse = true
		usedExtensions = true
	}

	data, err := c.conn.List("", "*", options).Collect()
	if err != nil && usedExtensions {
		// Older servers reject the LIST-EXTENDED options even when they
		// advertise only part of the extension. A plain LIST still gives the
		// caller mailbox names, but cannot filter by subscription.
		if subscribedOnly {
			return nil, fmt.Errorf("list subscribed mailboxes: %w", err)
		}
		data, err = c.conn.List("", "*", nil).Collect()
	}
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	mailboxes := make([]Mailbox, 0, len(data))
	for _, item := range data {
		mailbox := Mailbox{Name: item.Mailbox}
		if item.Delim != 0 {
			mailbox.Delimiter = string(item.Delim)
		}
		for _, attr := range item.Attrs {
			value := string(attr)
			mailbox.Attributes = append(mailbox.Attributes, value)
			if strings.EqualFold(value, string(imap.MailboxAttrSubscribed)) {
				mailbox.Subscribed = true
			}
			if isSpecialUse(value) {
				mailbox.SpecialUse = append(mailbox.SpecialUse, value)
			}
		}
		mailboxes = append(mailboxes, mailbox)
	}
	sort.Slice(mailboxes, func(i, j int) bool {
		return strings.ToLower(mailboxes[i].Name) < strings.ToLower(mailboxes[j].Name)
	})
	return mailboxes, nil
}

// Select selects a mailbox. When readOnly is set, EXAMINE is used.
func (c *Client) Select(mailbox string, readOnly bool) (*imap.SelectData, error) {
	data, err := c.conn.Select(mailbox, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return nil, fmt.Errorf("select %q: %w", mailbox, err)
	}
	return data, nil
}

// SearchUIDs runs a UID SEARCH and returns the matching UIDs in server order.
func (c *Client) SearchUIDs(criteria *imap.SearchCriteria) ([]imap.UID, error) {
	if criteria == nil {
		criteria = &imap.SearchCriteria{}
	}
	data, err := c.conn.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if data.All == nil {
		return nil, nil
	}
	set, ok := data.All.(imap.UIDSet)
	if !ok {
		return nil, fmt.Errorf("search returned an unexpected result type %T", data.All)
	}
	uids, resolved := set.Nums()
	if !resolved {
		return nil, errors.New("search returned an unresolved UID set")
	}
	return uids, nil
}

// Message contains the metadata and, when requested, the raw message.
type Message struct {
	UID          imap.UID
	SeqNum       uint32
	Flags        []string
	Size         int64
	InternalDate time.Time
	Envelope     *imap.Envelope
	Raw          []byte
}

// Summaries fetches metadata for the given UIDs. The result is in the order
// returned by the server.
func (c *Client) Summaries(uids []imap.UID) ([]Message, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	options := &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		Envelope:     true,
		InternalDate: true,
		RFC822Size:   true,
	}
	buffers, err := c.conn.Fetch(imap.UIDSetNum(uids...), options).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message metadata: %w", err)
	}
	messages := make([]Message, 0, len(buffers))
	for _, buffer := range buffers {
		messages = append(messages, messageFromBuffer(buffer))
	}
	return messages, nil
}

// FetchRaw fetches one complete message. When markSeen is false, BODY.PEEK is
// used so the server does not change the \Seen flag.
func (c *Client) FetchRaw(uid imap.UID, markSeen bool) (Message, error) {
	options := &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		Envelope:     true,
		InternalDate: true,
		RFC822Size:   true,
		BodySection: []*imap.FetchItemBodySection{{
			Peek: !markSeen,
		}},
	}
	buffers, err := c.conn.Fetch(imap.UIDSetNum(uid), options).Collect()
	if err != nil {
		return Message{}, fmt.Errorf("fetch UID %d: %w", uid, err)
	}
	if len(buffers) == 0 {
		return Message{}, fmt.Errorf("fetch UID %d: message not found", uid)
	}
	return messageFromBuffer(buffers[0]), nil
}

func messageFromBuffer(buffer *imapclient.FetchMessageBuffer) Message {
	message := Message{
		UID:          buffer.UID,
		SeqNum:       buffer.SeqNum,
		Size:         buffer.RFC822Size,
		InternalDate: buffer.InternalDate,
		Envelope:     buffer.Envelope,
		Flags:        make([]string, 0, len(buffer.Flags)),
	}
	for _, flag := range buffer.Flags {
		message.Flags = append(message.Flags, string(flag))
	}
	if len(buffer.BodySection) > 0 {
		message.Raw = buffer.BodySection[0].Bytes
	}
	return message
}

func supportsListExtended(caps imap.CapSet) bool {
	return caps.Has(imap.CapListExtended) || caps.Has(imap.CapIMAP4rev2)
}

func supportsSpecialUse(caps imap.CapSet) bool {
	return caps.Has(imap.CapSpecialUse) || caps.Has(imap.CapIMAP4rev2)
}

func isSpecialUse(attribute string) bool {
	switch strings.ToLower(attribute) {
	case `\all`, `\archive`, `\drafts`, `\flagged`, `\junk`, `\sent`, `\trash`, `\important`:
		return true
	default:
		return false
	}
}
