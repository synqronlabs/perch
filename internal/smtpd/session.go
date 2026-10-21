package smtpd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/raven/client"
	ravenmail "github.com/synqronlabs/raven/mail"
	"github.com/synqronlabs/raven/sasl"
	"github.com/synqronlabs/raven/server"
)

type session struct {
	srv      *Server
	conn     *server.Conn
	account  store.Account
	from     string
	rcpts    []string
	upstream *client.Client
	response string
}

func (s *session) AuthMechanisms() []string {
	return []string{"PLAIN", "LOGIN"}
}

func (s *session) Auth(mechanism string) (sasl.Server, error) {
	verify := func(creds *sasl.Credentials) error {
		key := remoteHost(s.conn) + "|" + strings.ToLower(strings.TrimSpace(creds.AuthenticationID))
		if !s.srv.login.Allowed(key) {
			return server.ErrAuthFailed
		}
		account, err := s.srv.store.AccountByAddress(context.Background(), creds.AuthenticationID)
		if err != nil {
			s.srv.login.Failure(key)
			return server.ErrAuthFailed
		}
		ok, err := password.Verify(account.PasswordHash, creds.Password)
		if err != nil || !ok {
			s.srv.login.Failure(key)
			return server.ErrAuthFailed
		}
		s.srv.login.Success(key)
		s.account = account
		return nil
	}
	switch strings.ToUpper(mechanism) {
	case "PLAIN":
		return sasl.NewPlainServer(verify), nil
	case "LOGIN":
		return sasl.NewLoginServer(verify), nil
	default:
		return nil, fmt.Errorf("unsupported authentication mechanism %q", mechanism)
	}
}

func (s *session) Mail(from string, opts *server.MailOptions) error {
	if s.account.ID == 0 {
		return server.ErrAuthRequired
	}
	if s.upstream != nil && (s.from != "" || len(s.rcpts) > 0) {
		_ = s.upstream.Reset()
		s.from = ""
		s.rcpts = nil
	}
	from = strings.TrimSpace(from)
	if from == "" {
		return &server.SMTPError{Code: 550, EnhancedCode: server.EnhancedCode{5, 7, 1}, Message: "null sender is not allowed on submission"}
	}
	normalized, err := store.NormalizeAddress(from)
	if err != nil {
		return server.ErrSenderDenied
	}
	ok, err := s.authorizedSender(normalized)
	if err != nil {
		return &server.SMTPError{Code: 451, EnhancedCode: server.EnhancedCode{4, 3, 0}, Message: "sender authorization unavailable"}
	}
	if !ok {
		return server.ErrSenderDenied
	}
	if err := s.ensureUpstream(); err != nil {
		return err
	}
	command := mailFromCommand(normalized, opts, s.upstream)
	if err := s.command(command); err != nil {
		return err
	}
	s.from = normalized
	s.rcpts = nil
	s.response = ""
	return nil
}

func (s *session) Rcpt(to string, _ *server.RcptOptions) error {
	if s.account.ID == 0 {
		return server.ErrAuthRequired
	}
	if s.from == "" {
		return &server.SMTPError{Code: 503, EnhancedCode: server.EnhancedCode{5, 5, 1}, Message: "MAIL FROM required"}
	}
	to = strings.TrimSpace(to)
	normalized, err := store.NormalizeAddress(to)
	if err != nil {
		return server.ErrRecipientDenied
	}
	if len(s.rcpts) >= s.srv.cfg.Limits.MaxRecipients {
		return server.ErrTooManyRecipients
	}
	if err := s.ensureUpstream(); err != nil {
		return err
	}
	if err := s.command("RCPT TO:<" + normalized + ">"); err != nil {
		return err
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}

func (s *session) Data(headers server.MessageHeaders, body io.Reader) error {
	if s.account.ID == 0 {
		return server.ErrAuthRequired
	}
	if s.from == "" || len(s.rcpts) == 0 {
		return &server.SMTPError{Code: 503, EnhancedCode: server.EnhancedCode{5, 5, 1}, Message: "MAIL FROM and RCPT TO required"}
	}
	if err := s.ensureUpstream(); err != nil {
		return err
	}
	headers = stripGeneratedReceived(headers)
	if !s.visibleSenderAuthorized(headers) {
		s.closeUpstream()
		return server.ErrSenderDenied
	}
	reader := &countingReader{reader: io.MultiReader(bytes.NewReader(headers), bytes.NewReader([]byte("\r\n")), body)}
	resp, err := s.upstream.StreamData(reader)
	if err != nil {
		s.closeUpstream()
		return temporaryError("upstream submission failed")
	}
	if resp == nil {
		s.closeUpstream()
		return temporaryError("upstream submission returned no response")
	}
	if !resp.IsSuccess() {
		s.closeUpstream()
		return responseError(resp)
	}
	ref := messageRef(resp.Message)
	if ref != "" {
		s.response = "message_ref=" + ref
	} else {
		s.response = "accepted"
	}
	parsedHeaders := ravenmail.ParseHeaders(headers)
	subject := parsedHeaders.Get("Subject")
	if ref != "" {
		if err := s.srv.store.UpsertOutboundAccepted(context.Background(), store.Outbound{
			AccountID: s.account.ID, MessageRef: ref, FromAddress: s.from,
			Recipients: append([]string(nil), s.rcpts...), Subject: subject,
			Size: reader.n, Status: "accepted", AcceptedAt: time.Now().UTC(),
		}); err != nil {
			s.srv.log.Warn("persist outbound acceptance", "message_ref", ref, "error", err)
		}
	}
	s.srv.log.Info("submission relayed", "account", s.account.PrimaryAddress,
		"from", s.from, "recipients", s.rcpts, "message_ref", ref, "bytes", reader.n,
		"remote", remoteAddr(s.conn))
	s.from = ""
	s.rcpts = nil
	return nil
}

func (s *session) DataSuccessResponse() string {
	if s.response == "" {
		return "accepted"
	}
	return s.response
}

func (s *session) Reset() {
	s.from = ""
	s.rcpts = nil
	s.response = ""
	if s.upstream != nil {
		if err := s.upstream.Reset(); err != nil {
			s.closeUpstream()
		}
	}
}

func (s *session) Logout() error {
	if s.upstream != nil {
		_ = s.upstream.Quit()
		s.closeUpstream()
	}
	return nil
}

func (s *session) ensureUpstream() error {
	if s.upstream != nil {
		return nil
	}
	listener, ok := s.srv.cfg.Listener("submission")
	if !ok {
		return temporaryError("submission listener is not configured")
	}
	dialer := client.NewDialer(s.srv.cfg.MXRaVen.SubmissionHost, s.srv.cfg.MXRaVen.SubmissionPort)
	dialer.StartTLS = true
	dialer.RequireTLS = true
	dialer.LocalName = s.srv.cfg.Hostname
	dialer.ConnectTimeout = 30 * time.Second
	dialer.ReadTimeout = 5 * time.Minute
	dialer.WriteTimeout = 5 * time.Minute
	dialer.Auth = &client.ClientAuth{Username: listener.KeyPrefix, Password: listener.Secret()}
	ctx := context.Background()
	if s.conn != nil {
		ctx = s.conn.Context()
	}
	upstream, err := dialer.DialContext(ctx)
	if err != nil {
		return temporaryError("cannot connect to mxRaven submission")
	}
	s.upstream = upstream
	return nil
}

func (s *session) closeUpstream() {
	if s.upstream != nil {
		_ = s.upstream.Close()
		s.upstream = nil
	}
}

func (s *session) command(command string) error {
	resp, err := s.upstream.RawCommand(command)
	if err != nil {
		s.closeUpstream()
		return temporaryError("upstream command failed")
	}
	if resp == nil {
		s.closeUpstream()
		return temporaryError("upstream command returned no response")
	}
	if !resp.IsSuccess() {
		return responseError(resp)
	}
	return nil
}

func (s *session) authorizedSender(address string) (bool, error) {
	addresses, err := s.srv.store.ListAddresses(context.Background(), s.account.ID)
	if err != nil {
		return false, err
	}
	for _, allowed := range addresses {
		if strings.HasPrefix(allowed, "*@") {
			if strings.HasSuffix(address, strings.ToLower(strings.TrimPrefix(allowed, "*"))) {
				return true, nil
			}
			continue
		}
		if strings.EqualFold(allowed, address) {
			return true, nil
		}
		if base, ok := stripPlus(address); ok && strings.EqualFold(allowed, base) {
			return true, nil
		}
	}
	return false, nil
}

func (s *session) visibleSenderAuthorized(headers []byte) bool {
	parsed := ravenmail.ParseHeaders(headers)
	value := parsed.Get("From")
	if strings.TrimSpace(value) == "" {
		return false
	}
	list, err := mail.ParseAddressList(value)
	if err != nil || len(list) == 0 {
		return false
	}
	for _, address := range list {
		normalized, err := store.NormalizeAddress(address.Address)
		if err != nil {
			return false
		}
		ok, err := s.authorizedSender(normalized)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func mailFromCommand(from string, opts *server.MailOptions, upstream *client.Client) string {
	command := "MAIL FROM:<" + from + ">"
	var params []string
	if opts != nil {
		if opts.Size > 0 && upstream.HasExtension(ravenmail.ExtSize) {
			params = append(params, "SIZE="+strconv.FormatInt(opts.Size, 10))
		}
		if opts.Body == server.Body8BitMIME && upstream.HasExtension(ravenmail.Ext8BitMIME) {
			params = append(params, "BODY=8BITMIME")
		}
		if opts.UTF8 && upstream.HasExtension(ravenmail.ExtSMTPUTF8) {
			params = append(params, "SMTPUTF8")
		}
		if opts.RequireTLS && upstream.HasExtension(ravenmail.ExtRequireTLS) {
			params = append(params, "REQUIRETLS")
		}
	}
	if len(params) > 0 {
		command += " " + strings.Join(params, " ")
	}
	return command
}

func responseError(resp *client.ClientResponse) *server.SMTPError {
	code := resp.Code
	if code < 400 || code > 599 {
		code = 451
	}
	return &server.SMTPError{
		Code:         code,
		EnhancedCode: parseEnhancedCode(resp.EnhancedCode),
		Message:      resp.Message,
	}
}

func temporaryError(message string) *server.SMTPError {
	return &server.SMTPError{
		Code:         451,
		EnhancedCode: server.EnhancedCode{4, 3, 0},
		Message:      message,
	}
}

func parseEnhancedCode(value string) server.EnhancedCode {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return server.NoEnhancedCode
	}
	var code server.EnhancedCode
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 9 {
			return server.NoEnhancedCode
		}
		code[i] = n
	}
	return code
}

func messageRef(response string) string {
	const key = "message_ref="
	index := strings.Index(response, key)
	if index < 0 {
		return ""
	}
	value := strings.TrimSpace(response[index+len(key):])
	value = strings.TrimPrefix(value, "<")
	if end := strings.IndexAny(value, ">; \t\r\n"); end >= 0 {
		value = value[:end]
	}
	return value
}

type countingReader struct {
	reader io.Reader
	n      int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.n += int64(n)
	return n, err
}

func stripGeneratedReceived(headers []byte) []byte {
	if !bytes.HasPrefix(headers, []byte("Received:")) {
		return headers
	}
	offset := 0
	for {
		next := bytes.Index(headers[offset:], []byte("\r\n"))
		if next < 0 {
			return nil
		}
		offset += next + 2
		if offset >= len(headers) {
			return nil
		}
		if headers[offset] != ' ' && headers[offset] != '\t' {
			return headers[offset:]
		}
	}
}

func stripPlus(address string) (string, bool) {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return address, false
	}
	local := address[:at]
	plus := strings.LastIndexByte(local, '+')
	if plus < 0 {
		return address, false
	}
	return local[:plus] + address[at:], true
}

func remoteAddr(conn *server.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	return conn.RemoteAddr().String()
}

func remoteHost(conn *server.Conn) string {
	addr := remoteAddr(conn)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
