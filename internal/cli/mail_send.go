package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"

	"github.com/synqronlabs/perch/internal/smtpclient"
	cli "github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func mailSendCommand() *cli.Command {
	return &cli.Command{
		Name:                      "send",
		Usage:                     "compose and send a message",
		DisableSliceFlagSeparator: true,
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "from", Usage: "sender address (default: --username)"},
			&cli.StringSliceFlag{Name: "to", Usage: "recipient address or list; repeatable"},
			&cli.StringSliceFlag{Name: "cc", Usage: "Cc recipient address or list; repeatable"},
			&cli.StringSliceFlag{Name: "bcc", Usage: "Bcc recipient address or list; repeatable"},
			&cli.StringSliceFlag{Name: "reply-to", Usage: "Reply-To address or list; repeatable"},
			&cli.StringFlag{Name: "subject", Aliases: []string{"s"}, Usage: "message subject"},
			&cli.StringFlag{Name: "body", Usage: "plain-text body"},
			&cli.StringFlag{Name: "body-file", Usage: "read the plain-text body from a file (use - for stdin)"},
			&cli.StringFlag{Name: "html-body", Usage: "HTML body"},
			&cli.StringFlag{Name: "html-file", Usage: "read the HTML body from a file (use - for stdin)"},
			&cli.StringSliceFlag{Name: "attach", Aliases: []string{"a"}, Usage: "attach a file; repeatable"},
			&cli.StringSliceFlag{Name: "header", Aliases: []string{"H"}, Usage: "add an extra header (Name: Value); repeatable"},
			&cli.StringFlag{Name: "in-reply-to", Usage: "Message-ID being replied to"},
			&cli.StringFlag{Name: "references", Usage: "space-separated Message-IDs for References"},
			&cli.BoolFlag{Name: "8bitmime", Usage: "send UTF-8 text with BODY=8BITMIME"},
			&cli.BoolFlag{Name: "smtputf8", Usage: "force SMTPUTF8 (usually detected automatically)"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"n"}, Usage: "write the composed message to stdout without sending"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Usage: "print SMTP capabilities and delivery details to stderr"},
		}, smtpConnectionFlags()...),
		UsageText: `perch mail send [flags]

The body is read from --body, --body-file, --html-body, --html-file, or stdin
when stdin is not a terminal. Provide both a text and an HTML body to send
multipart/alternative. Attachments are base64 encoded and sent as
multipart/mixed.

Examples:
  perch mail send --from you@example.com --to alice@example.com \
    --subject 'Hello' --body 'Hi Alice'
  echo 'Body from stdin' | perch mail send --from you@example.com \
    --to alice@example.com --subject 'Hello'
  perch mail send --from you@example.com --to alice@example.com \
    --subject 'Files' --body 'See attached.' --attach report.pdf
  perch mail send --from you@example.com --to alice@example.com \
    --subject 'Hello' --dry-run`,
		Action: runMailSend,
	}
}

func mailCapabilitiesCommand() *cli.Command {
	return &cli.Command{
		Name:    "caps",
		Aliases: []string{"capabilities"},
		Usage:   "show the SMTP extensions advertised by a submission server",
		Flags:   smtpConnectionFlags(),
		UsageText: `perch mail caps [flags]

Prints the EHLO extensions of the configured submission server. Use
--smtp-server to inspect another server, for example mxRaven:
  perch mail caps --smtp-server smtp.mxraven.email:587`,
		Action: runMailCapabilities,
	}
}

func runMailCapabilities(ctx context.Context, cmd *cli.Command) error {
	opts, err := smtpClientOptions(cmd, false)
	if err != nil {
		return err
	}
	client, err := dialSMTP(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	for _, capability := range client.Capabilities() {
		fmt.Println(capability)
	}
	return nil
}

func dialSMTP(ctx context.Context, opts smtpclient.Options) (*smtpclient.Client, error) {
	client, err := smtpclient.Dial(ctx, opts)
	if err == nil {
		return client, nil
	}
	if opts.TLSMode != smtpclient.TLSDisabled && isLoopbackAddress(opts.Addr) &&
		strings.Contains(strings.ToUpper(err.Error()), "TLS") {
		return nil, fmt.Errorf("%w (hint: use --smtp-plaintext for a loopback submission listener)", err)
	}
	return nil, err
}

func runMailSend(ctx context.Context, cmd *cli.Command) error {
	from, err := resolveSendSender(cmd)
	if err != nil {
		return err
	}
	to, err := parseSendAddresses(cmd.StringSlice("to"), "to")
	if err != nil {
		return err
	}
	cc, err := parseSendAddresses(cmd.StringSlice("cc"), "cc")
	if err != nil {
		return err
	}
	bcc, err := parseSendAddresses(cmd.StringSlice("bcc"), "bcc")
	if err != nil {
		return err
	}
	replyTo, err := parseSendAddresses(cmd.StringSlice("reply-to"), "reply-to")
	if err != nil {
		return err
	}
	recipients := append(append(append([]*mail.Address(nil), to...), cc...), bcc...)
	if len(recipients) == 0 {
		return errors.New("at least one --to, --cc, or --bcc recipient is required")
	}
	text, hasText, html, hasHTML, err := resolveSendBody(cmd)
	if err != nil {
		return err
	}
	headers, err := parseSendHeaders(cmd.StringSlice("header"))
	if err != nil {
		return err
	}
	raw, err := composeMessage(composeOptions{
		From:        from,
		To:          to,
		Cc:          cc,
		Bcc:         bcc,
		ReplyTo:     replyTo,
		Subject:     cmd.String("subject"),
		Text:        text,
		HTML:        html,
		HasText:     hasText,
		HasHTML:     hasHTML,
		Attachments: cmd.StringSlice("attach"),
		Headers:     headers,
		InReplyTo:   cmd.String("in-reply-to"),
		References:  strings.Fields(cmd.String("references")),
		EightBit:    cmd.Bool("8bitmime"),
	})
	if err != nil {
		return err
	}
	if cmd.Bool("dry-run") {
		_, err := os.Stdout.Write(raw)
		return err
	}

	opts, err := smtpClientOptions(cmd, true)
	if err != nil {
		return err
	}
	client, err := dialSMTP(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if cmd.Bool("verbose") {
		fmt.Fprintln(os.Stderr, "SMTP capabilities:")
		for _, capability := range client.Capabilities() {
			fmt.Fprintf(os.Stderr, "  %s\n", capability)
		}
	}

	requiredUTF8 := requiresSMTPUTF8(from, recipients, headers)
	useUTF8 := requiredUTF8
	if cmd.IsSet("smtputf8") {
		if !cmd.Bool("smtputf8") && requiredUTF8 {
			return errors.New("message requires SMTPUTF8 but --smtputf8=false was set")
		}
		useUTF8 = cmd.Bool("smtputf8")
	}
	if useUTF8 && !client.HasExtension("SMTPUTF8") {
		return errors.New("message requires SMTPUTF8 but the SMTP server does not advertise it")
	}
	if cmd.Bool("8bitmime") && !client.HasExtension("8BITMIME") {
		return errors.New("--8bitmime was requested but the SMTP server does not advertise 8BITMIME")
	}

	envelope := smtpclient.Envelope{
		From:       from.Address,
		Recipients: uniqueRecipients(recipients),
		SMTPUTF8:   useUTF8,
		Body8Bit:   cmd.Bool("8bitmime"),
	}
	if cmd.Bool("verbose") {
		fmt.Fprintf(os.Stderr, "sending %d bytes from %s to %d recipient(s)\n",
			len(raw), envelope.From, len(envelope.Recipients))
	}
	messageID, err := client.Send(envelope, raw)
	if err != nil {
		return err
	}
	if cmd.Bool("verbose") && messageID != "" {
		fmt.Fprintf(os.Stderr, "server message id: %s\n", messageID)
	}
	return nil
}

func resolveSendSender(cmd *cli.Command) (*mail.Address, error) {
	value := strings.TrimSpace(cmd.String("from"))
	if value == "" {
		value = strings.TrimSpace(cmd.String("username"))
	}
	if value == "" {
		value = strings.TrimSpace(os.Getenv("PERCH_IMAP_USER"))
	}
	if value == "" {
		return nil, errors.New("--from is required (or set --username/PERCH_IMAP_USER)")
	}
	address, err := mail.ParseAddress(value)
	if err != nil {
		return nil, fmt.Errorf("parse --from address %q: %w", value, err)
	}
	return address, nil
}

func parseSendAddresses(values []string, label string) ([]*mail.Address, error) {
	var addresses []*mail.Address
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := mail.ParseAddressList(value)
		if err != nil {
			return nil, fmt.Errorf("parse --%s address %q: %w", label, value, err)
		}
		addresses = append(addresses, parsed...)
	}
	return addresses, nil
}

func uniqueRecipients(addresses []*mail.Address) []string {
	seen := make(map[string]struct{}, len(addresses))
	unique := make([]string, 0, len(addresses))
	for _, address := range addresses {
		key := strings.ToLower(address.Address)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, address.Address)
	}
	return unique
}

func requiresSMTPUTF8(from *mail.Address, recipients []*mail.Address, headers []headerField) bool {
	if containsNonASCII(from.Address) {
		return true
	}
	for _, recipient := range recipients {
		if containsNonASCII(recipient.Address) {
			return true
		}
	}
	for _, header := range headers {
		if containsNonASCII(header.Value) {
			return true
		}
	}
	return false
}

func resolveSendBody(cmd *cli.Command) (string, bool, string, bool, error) {
	if cmd.Bool("password-stdin") {
		if cmd.String("body-file") == "-" || cmd.String("html-file") == "-" {
			return "", false, "", false, errors.New("stdin cannot be both the password and the message body")
		}
		if !cmd.IsSet("body") && !cmd.IsSet("body-file") && !cmd.IsSet("html-body") && !cmd.IsSet("html-file") {
			return "", false, "", false, errors.New("provide --body, --body-file, --html-body, or --html-file when using --password-stdin")
		}
	}
	text, hasText, err := readSendBody(cmd, "body", "body-file")
	if err != nil {
		return "", false, "", false, err
	}
	html, hasHTML, err := readSendBody(cmd, "html-body", "html-file")
	if err != nil {
		return "", false, "", false, err
	}
	if !hasText && !hasHTML {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return "", false, "", false, nil
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", false, "", false, fmt.Errorf("read body from stdin: %w", err)
		}
		text, hasText = string(data), true
	}
	return text, hasText, html, hasHTML, nil
}

func readSendBody(cmd *cli.Command, inlineName, fileName string) (string, bool, error) {
	inlineSet := cmd.IsSet(inlineName)
	fileSet := cmd.IsSet(fileName)
	if inlineSet && fileSet {
		return "", false, fmt.Errorf("use only one of --%s and --%s", inlineName, fileName)
	}
	if fileSet {
		path := cmd.String(fileName)
		if path == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return "", false, fmt.Errorf("read --%s from stdin: %w", fileName, err)
			}
			return string(data), true, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Errorf("read --%s: %w", fileName, err)
		}
		return string(data), true, nil
	}
	if inlineSet {
		return cmd.String(inlineName), true, nil
	}
	return "", false, nil
}

func parseSendHeaders(values []string) ([]headerField, error) {
	headers := make([]headerField, 0, len(values))
	for _, value := range values {
		name, headerValue, ok := strings.Cut(value, ":")
		if !ok {
			return nil, fmt.Errorf("header %q must use Name: Value", value)
		}
		name = strings.TrimSpace(name)
		headerValue = strings.TrimSpace(headerValue)
		if err := validateHeader(name, headerValue); err != nil {
			return nil, err
		}
		if reservedHeader(name) {
			return nil, fmt.Errorf("header %q is managed by perch; use the matching flag", name)
		}
		headers = append(headers, headerField{Name: name, Value: headerValue})
	}
	return headers, nil
}

func reservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "from", "to", "cc", "bcc", "reply-to", "subject", "date", "message-id",
		"mime-version", "content-type", "content-transfer-encoding", "in-reply-to", "references":
		return true
	default:
		return false
	}
}
