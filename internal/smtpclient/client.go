// Package smtpclient is the small SMTP submission client used by the perch
// CLI. It wraps Raven's SMTP client and exposes only the operations needed to
// send a composed message.
package smtpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	ravenclient "github.com/synqronlabs/raven/client"
	ravenmail "github.com/synqronlabs/raven/mail"
)

// TLSMode selects how the client secures its SMTP connection.
type TLSMode int

const (
	// TLSImplicit connects with TLS immediately (usually port 465).
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
	LocalName string
	Timeout   time.Duration
}

// Client wraps Raven's SMTP client.
type Client struct {
	conn *ravenclient.Client
}

// Dial connects and optionally authenticates to an SMTP submission server.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	host, portText, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("parse SMTP server address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("parse SMTP server port %q: %w", portText, err)
	}
	if opts.Username != "" && opts.Password == "" {
		return nil, errors.New("SMTP password is required when a username is set")
	}

	dialer := ravenclient.NewDialer(host, port)
	dialer.TLSConfig = opts.TLSConfig
	dialer.LocalName = opts.LocalName
	dialer.ConnectTimeout = opts.Timeout
	if dialer.ConnectTimeout <= 0 {
		dialer.ConnectTimeout = 30 * time.Second
	}
	switch opts.TLSMode {
	case TLSImplicit:
		dialer.SSL = true
	case TLSStartTLS:
		dialer.StartTLS = true
		dialer.RequireTLS = true
	case TLSDisabled:
	default:
		return nil, fmt.Errorf("unknown SMTP TLS mode %d", opts.TLSMode)
	}
	if opts.Username != "" {
		dialer.Auth = &ravenclient.ClientAuth{
			Username: opts.Username,
			Password: opts.Password,
		}
	}
	conn, err := dialer.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to SMTP server %s: %w", opts.Addr, err)
	}
	return &Client{conn: conn}, nil
}

// Close ends the SMTP session.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Quit()
}

// Capabilities returns the extensions advertised by the server in a stable
// order, including any parameter (for example "SIZE 26214400").
func (c *Client) Capabilities() []string {
	extensions := c.conn.Extensions()
	caps := make([]string, 0, len(extensions))
	for name, value := range extensions {
		if value == "" {
			caps = append(caps, string(name))
		} else {
			caps = append(caps, string(name)+" "+value)
		}
	}
	sort.Slice(caps, func(i, j int) bool {
		return strings.ToUpper(caps[i]) < strings.ToUpper(caps[j])
	})
	return caps
}

// HasExtension reports whether the server advertised an extension.
func (c *Client) HasExtension(name string) bool {
	return c.conn.HasExtension(ravenmail.Extension(name))
}

// MaxSize returns the server's advertised SIZE limit, or zero when unlimited
// or not advertised.
func (c *Client) MaxSize() int64 {
	return c.conn.MaxSize()
}

// Envelope is the SMTP transport envelope for a message.
type Envelope struct {
	From       string
	Recipients []string
	SMTPUTF8   bool
	Body8Bit   bool
}

// Send delivers raw to the server and returns any server-assigned message ID.
func (c *Client) Send(envelope Envelope, raw []byte) (string, error) {
	from, err := ravenmail.ParseAddress(envelope.From)
	if err != nil {
		return "", fmt.Errorf("invalid sender address %q: %w", envelope.From, err)
	}
	if len(envelope.Recipients) == 0 {
		return "", errors.New("at least one recipient is required")
	}
	smtpEnvelope := ravenmail.Envelope{
		From: ravenmail.Path{Mailbox: from},
		Size: int64(len(raw)),
	}
	for _, recipient := range envelope.Recipients {
		address, err := ravenmail.ParseAddress(recipient)
		if err != nil {
			return "", fmt.Errorf("invalid recipient address %q: %w", recipient, err)
		}
		smtpEnvelope.To = append(smtpEnvelope.To, ravenmail.Recipient{
			Address: ravenmail.Path{Mailbox: address},
		})
	}
	if envelope.SMTPUTF8 {
		if !c.HasExtension("SMTPUTF8") {
			return "", errors.New("SMTP server does not advertise SMTPUTF8")
		}
		smtpEnvelope.SMTPUTF8 = true
	}
	if envelope.Body8Bit {
		if !c.HasExtension("8BITMIME") {
			return "", errors.New("SMTP server does not advertise 8BITMIME")
		}
		smtpEnvelope.BodyType = ravenmail.BodyType8BitMIME
	}
	if max := c.MaxSize(); max > 0 && int64(len(raw)) > max {
		return "", fmt.Errorf("message is %d bytes but the SMTP server limit is %d", len(raw), max)
	}
	result, err := c.conn.SendRaw(smtpEnvelope, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	if result == nil {
		return "", errors.New("send message: SMTP server returned no response")
	}
	return result.MessageID, nil
}
