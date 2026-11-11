package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/synqronlabs/perch/internal/mailclient"
	cli "github.com/urfave/cli/v3"
)

func mailCommand() *cli.Command {
	return &cli.Command{
		Name:  "mail",
		Usage: "list, search, fetch, and send mail",
		UsageText: `perch mail list   [connection flags]
perch mail search [connection flags] [--mailbox INBOX] [--query EXPR] [--uids SET]
perch mail fetch  [connection flags] [--mailbox INBOX] [--query EXPR] [--uids SET] [--all]
perch mail send   [smtp flags] --to ADDR [--subject SUBJECT] [--body TEXT] [--attach FILE]
perch mail caps   [smtp flags]`,
		Action: func(_ context.Context, cmd *cli.Command) error {
			if name := cmd.Args().First(); name != "" {
				return unknownCommandError(cmd, name)
			}
			return cli.Exit("usage: perch mail list|search|fetch|send|caps", 1)
		},
		Suggest:      true,
		OnUsageError: commandUsageError,
		Flags:        mailCommonFlags(),
		Commands: []*cli.Command{
			mailListCommand(),
			mailSearchCommand(),
			mailFetchCommand(),
			mailSendCommand(),
			mailCapabilitiesCommand(),
		},
	}
}

func mailCommonFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "username", Aliases: []string{"u"}, Usage: "account username (env PERCH_IMAP_USER)"},
		&cli.StringFlag{Name: "password", Usage: "account password (prefer PERCH_IMAP_PASSWORD or --password-file)"},
		&cli.StringFlag{Name: "password-file", Usage: "read the account password from a file"},
		&cli.BoolFlag{Name: "password-stdin", Usage: "read the account password from stdin"},
		&cli.DurationFlag{Name: "timeout", Value: 30 * time.Second, Usage: "connection timeout"},
		configFlag(defaultConfigPath()),
	}
}

func imapConnectionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "IMAP server (imaps://host, imap://host, or host[:port]); defaults from --config",
		},
		&cli.BoolFlag{Name: "plaintext", Usage: "connect to IMAP without TLS (use only on trusted networks)"},
		&cli.BoolFlag{Name: "starttls", Usage: "require STARTTLS for IMAP"},
		&cli.BoolFlag{Name: "insecure", Usage: "skip IMAP TLS certificate verification"},
		&cli.StringFlag{Name: "ca-file", Usage: "PEM CA bundle used to verify the IMAP server certificate"},
	}
}

func mailListCommand() *cli.Command {
	return &cli.Command{
		Name:    "list",
		Aliases: []string{"ls", "mailboxes", "folders"},
		Usage:   "list mailboxes (folders), not messages",
		Flags: append([]cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "write one JSON object per mailbox"},
			&cli.BoolFlag{Name: "subscribed", Usage: "only list subscribed mailboxes"},
		}, imapConnectionFlags()...),
		Action: runMailList,
	}
}

func mailSearchCommand() *cli.Command {
	return &cli.Command{
		Name:    "search",
		Aliases: []string{"find"},
		Usage:   "search message metadata",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "mailbox", Aliases: []string{"m"}, Value: "INBOX", Usage: "mailbox to search"},
			&cli.StringFlag{Name: "query", Aliases: []string{"q"}, Usage: "search expression (see the examples in --help)"},
			&cli.StringFlag{Name: "uids", Usage: "explicit UID set, for example 1,3:5,10:*"},
			&cli.IntFlag{Name: "limit", Aliases: []string{"n"}, Value: 50, Usage: "maximum results (0 means unlimited)"},
			&cli.BoolFlag{Name: "reverse", Usage: "oldest first instead of newest first"},
			&cli.BoolFlag{Name: "json", Usage: "write one JSON object per message"},
		}, imapConnectionFlags()...),
		UsageText: `perch mail search [flags] [query]

Query terms are joined with AND. Bare words search the message text.
Supported keys: from to cc bcc subject body text header:Name=Value
since before on sent-since sent-before sent-on seen unseen answered
unanswered flagged unflagged deleted undeleted draft undraft larger
smaller uid flag/keyword unflag/unkeyword.

Examples:
  perch mail search -q 'from:alice since:2026-01-01'
  perch mail search -q 'unseen larger:1M' --json
  perch mail search --uids 12,14 --limit 0`,
		Action: runMailSearch,
	}
}

func mailFetchCommand() *cli.Command {
	return &cli.Command{
		Name:    "fetch",
		Aliases: []string{"get"},
		Usage:   "fetch raw messages",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "mailbox", Aliases: []string{"m"}, Value: "INBOX", Usage: "mailbox to fetch from"},
			&cli.StringFlag{Name: "query", Aliases: []string{"q"}, Usage: "search expression"},
			&cli.StringFlag{Name: "uids", Usage: "explicit UID set, for example 1,3:5,10:*"},
			&cli.BoolFlag{Name: "all", Usage: "fetch every message in the mailbox"},
			&cli.IntFlag{Name: "limit", Aliases: []string{"n"}, Value: 0, Usage: "maximum messages (0 means unlimited)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"f"}, Value: "eml", Usage: "eml, mbox, or maildir"},
			&cli.StringFlag{Name: "dest", Aliases: []string{"d", "o"}, Usage: "destination directory (eml/maildir) or file (mbox); default stdout"},
			&cli.BoolFlag{Name: "mark-seen", Usage: "let the server set \\Seen (default uses BODY.PEEK)"},
			&cli.BoolFlag{Name: "overwrite", Usage: "overwrite an existing eml/mbox destination"},
			&cli.BoolFlag{Name: "reverse", Usage: "fetch newest first instead of oldest first"},
		}, imapConnectionFlags()...),
		UsageText: `perch mail fetch [flags] [query]

Fetching to stdout is only allowed for one eml message or for mbox format.
Use --dest with eml (a directory) or maildir (a Maildir root).

Examples:
  perch mail fetch --uids 42
  perch mail fetch -q 'from:alice' --format mbox > alice.mbox
  perch mail fetch -q 'unseen' --format maildir --dest ~/Mail
  perch mail fetch --uids 42 --dest ./messages`,
		Action: runMailFetch,
	}
}

func runMailList(_ context.Context, cmd *cli.Command) error {
	client, err := openMailClient(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	mailboxes, err := client.Mailboxes(cmd.Bool("subscribed"))
	if err != nil {
		return err
	}
	if cmd.Bool("json") {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		for _, mailbox := range mailboxes {
			if err := encoder.Encode(mailboxJSON{
				Name:       mailbox.Name,
				Delimiter:  mailbox.Delimiter,
				Attributes: mailbox.Attributes,
				SpecialUse: mailbox.SpecialUse,
				Subscribed: mailbox.Subscribed,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	rows := make([][]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		rows = append(rows, []string{
			mailbox.Name,
			strings.Join(mailbox.SpecialUse, ","),
			fmt.Sprint(mailbox.Subscribed),
		})
	}
	return printTable([]string{"NAME", "SPECIAL-USE", "SUBSCRIBED"}, rows)
}

func runMailSearch(_ context.Context, cmd *cli.Command) error {
	criteria, err := searchCriteria(cmd)
	if err != nil {
		return err
	}
	client, err := openMailClient(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	mailbox := cmd.String("mailbox")
	if _, err := client.Select(mailbox, true); err != nil {
		return err
	}
	uids, err := client.SearchUIDs(criteria)
	if err != nil {
		return err
	}
	uids = orderUIDs(uids, !cmd.Bool("reverse"), cmd.Int("limit"))
	if len(uids) == 0 {
		return nil
	}
	messages, err := client.Summaries(uids)
	if err != nil {
		return err
	}
	byUID := make(map[imap.UID]mailclient.Message, len(messages))
	for _, message := range messages {
		byUID[message.UID] = message
	}
	if cmd.Bool("json") {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		for _, uid := range uids {
			message, ok := byUID[uid]
			if !ok {
				continue
			}
			if err := encoder.Encode(messageJSON(mailbox, message)); err != nil {
				return err
			}
		}
		return nil
	}
	rows := make([][]string, 0, len(uids))
	for _, uid := range uids {
		message, ok := byUID[uid]
		if !ok {
			continue
		}
		envelope := message.Envelope
		subject := ""
		sender := ""
		if envelope != nil {
			subject = envelope.Subject
			sender = formatAddresses(envelope.From)
		}
		rows = append(rows, []string{
			fmt.Sprint(message.UID),
			formatMessageDate(message),
			sender,
			subject,
			fmt.Sprint(message.Size),
			strings.Join(message.Flags, ","),
		})
	}
	return printTable([]string{"UID", "DATE", "FROM", "SUBJECT", "SIZE", "FLAGS"}, rows)
}

func runMailFetch(_ context.Context, cmd *cli.Command) error {
	format := strings.ToLower(cmd.String("format"))
	switch format {
	case "eml", "mbox", "maildir":
	default:
		return fmt.Errorf("unknown format %q (use eml, mbox, or maildir)", cmd.String("format"))
	}
	query := strings.TrimSpace(strings.Join(cmd.Args().Slice(), " "))
	if cmd.String("query") != "" {
		if query != "" {
			query += " "
		}
		query += cmd.String("query")
	}
	if strings.TrimSpace(query) == "" && cmd.String("uids") == "" && !cmd.Bool("all") {
		return errors.New("refusing to fetch an entire mailbox; pass --uids, a query, or --all")
	}
	criteria, err := searchCriteria(cmd)
	if err != nil {
		return err
	}

	client, err := openMailClient(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	mailbox := cmd.String("mailbox")
	markSeen := cmd.Bool("mark-seen")
	if _, err := client.Select(mailbox, !markSeen); err != nil {
		return err
	}
	uids, err := client.SearchUIDs(criteria)
	if err != nil {
		return err
	}
	uids = orderUIDs(uids, cmd.Bool("reverse"), cmd.Int("limit"))
	if len(uids) == 0 {
		return nil
	}

	dest := cmd.String("dest")
	if format == "maildir" && (dest == "" || dest == "-") {
		return errors.New("--dest is required with --format maildir")
	}
	if format == "eml" && (dest == "" || dest == "-") && len(uids) != 1 {
		return errors.New("refusing to write multiple messages to stdout; use --format mbox or --dest")
	}
	if format == "eml" && dest != "" && dest != "-" {
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return fmt.Errorf("create destination: %w", err)
		}
	}
	if format == "maildir" {
		if err := prepareMaildir(dest); err != nil {
			return err
		}
	}

	var mboxWriter io.Writer = os.Stdout
	if format == "mbox" && dest != "" && dest != "-" {
		file, err := openOutputFile(dest, cmd.Bool("overwrite"))
		if err != nil {
			return err
		}
		buffered := bufio.NewWriter(file)
		defer func() {
			_ = buffered.Flush()
			_ = file.Close()
		}()
		mboxWriter = buffered
	}

	for _, uid := range uids {
		message, err := client.FetchRaw(uid, markSeen)
		if err != nil {
			return err
		}
		switch format {
		case "eml":
			if dest == "" || dest == "-" {
				if _, err := os.Stdout.Write(message.Raw); err != nil {
					return err
				}
				continue
			}
			path := filepath.Join(dest, fmt.Sprintf("%d.eml", uid))
			if err := writeMessageFile(path, message.Raw, cmd.Bool("overwrite")); err != nil {
				return err
			}
		case "mbox":
			if err := writeMboxMessage(mboxWriter, message); err != nil {
				return err
			}
		case "maildir":
			if err := writeMaildirMessage(dest, message); err != nil {
				return err
			}
		}
	}
	return nil
}

func searchCriteria(cmd *cli.Command) (*imap.SearchCriteria, error) {
	var parts []string
	if query := strings.TrimSpace(cmd.String("query")); query != "" {
		parts = append(parts, query)
	}
	parts = append(parts, cmd.Args().Slice()...)
	criteria, err := mailclient.ParseQuery(strings.Join(parts, " "))
	if err != nil {
		return nil, err
	}
	if value := cmd.String("uids"); value != "" {
		set, err := mailclient.ParseUIDSet(value)
		if err != nil {
			return nil, err
		}
		criteria.UID = append(criteria.UID, set)
	}
	return criteria, nil
}

func orderUIDs(uids []imap.UID, newestFirst bool, limit int) []imap.UID {
	ordered := append([]imap.UID(nil), uids...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i] < ordered[j]
	})
	if newestFirst {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}
