package cli

import (
	"bufio"
	"bytes"
	"crypto/rand"
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
)

type mailboxJSON struct {
	Name       string   `json:"name"`
	Delimiter  string   `json:"delimiter,omitempty"`
	Attributes []string `json:"attributes,omitempty"`
	SpecialUse []string `json:"special_use,omitempty"`
	Subscribed bool     `json:"subscribed"`
}

type addressJSON struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type messageJSONValue struct {
	UID          uint32        `json:"uid"`
	Seq          uint32        `json:"seq,omitempty"`
	Mailbox      string        `json:"mailbox"`
	Subject      string        `json:"subject,omitempty"`
	From         []addressJSON `json:"from,omitempty"`
	To           []addressJSON `json:"to,omitempty"`
	Cc           []addressJSON `json:"cc,omitempty"`
	Date         string        `json:"date,omitempty"`
	InternalDate string        `json:"internal_date,omitempty"`
	Size         int64         `json:"size,omitempty"`
	Flags        []string      `json:"flags,omitempty"`
	MessageID    string        `json:"message_id,omitempty"`
}

func messageJSON(mailbox string, message mailclient.Message) messageJSONValue {
	value := messageJSONValue{
		UID:     uint32(message.UID),
		Seq:     message.SeqNum,
		Mailbox: mailbox,
		Size:    message.Size,
		Flags:   message.Flags,
	}
	if date := messageTime(message); !date.IsZero() {
		value.Date = date.Format(time.RFC3339)
	}
	if !message.InternalDate.IsZero() {
		value.InternalDate = message.InternalDate.Format(time.RFC3339)
	}
	if envelope := message.Envelope; envelope != nil {
		value.Subject = envelope.Subject
		value.From = addressesJSON(envelope.From)
		value.To = addressesJSON(envelope.To)
		value.Cc = addressesJSON(envelope.Cc)
		value.MessageID = envelope.MessageID
	}
	return value
}

func addressesJSON(addresses []imap.Address) []addressJSON {
	out := make([]addressJSON, 0, len(addresses))
	for i := range addresses {
		address := &addresses[i]
		if address.IsGroupStart() || address.IsGroupEnd() {
			continue
		}
		addr := address.Addr()
		if addr == "" {
			continue
		}
		out = append(out, addressJSON{Name: address.Name, Address: addr})
	}
	return out
}

func formatAddresses(addresses []imap.Address) string {
	parts := make([]string, 0, len(addresses))
	for i := range addresses {
		address := &addresses[i]
		if address.IsGroupStart() || address.IsGroupEnd() {
			continue
		}
		addr := address.Addr()
		if addr == "" {
			continue
		}
		if address.Name != "" {
			parts = append(parts, address.Name+" <"+addr+">")
		} else {
			parts = append(parts, addr)
		}
	}
	return strings.Join(parts, ", ")
}

func messageTime(message mailclient.Message) time.Time {
	if message.Envelope != nil && !message.Envelope.Date.IsZero() {
		return message.Envelope.Date
	}
	return message.InternalDate
}

func formatMessageDate(message mailclient.Message) string {
	date := messageTime(message)
	if date.IsZero() {
		return ""
	}
	return date.Format("2006-01-02 15:04")
}

func mboxDate(message mailclient.Message) time.Time {
	if date := messageTime(message); !date.IsZero() {
		return date
	}
	return time.Now()
}

func writeMboxMessage(w io.Writer, message mailclient.Message) error {
	sender := "MAILER-DAEMON"
	if message.Envelope != nil {
		for i := range message.Envelope.From {
			if address := message.Envelope.From[i].Addr(); address != "" {
				sender = address
				break
			}
		}
	}
	if _, err := fmt.Fprintf(w, "From %s %s\n", sender, mboxDate(message).Format("Mon Jan _2 15:04:05 2006")); err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(message.Raw))
	scanner.Buffer(make([]byte, 64*1024), len(message.Raw)+1)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.HasPrefix(line, "From ") {
			line = ">" + line
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("write mbox message: %w", err)
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func writeMessageFile(path string, raw []byte, overwrite bool) error {
	file, err := openOutputFile(path, overwrite)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func openOutputFile(path string, overwrite bool) (*os.File, error) {
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%s already exists (use --overwrite)", path)
		}
		return nil, err
	}
	return file, nil
}

func prepareMaildir(root string) error {
	for _, name := range []string{"tmp", "new", "cur"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			return fmt.Errorf("create maildir: %w", err)
		}
	}
	return nil
}

func writeMaildirMessage(root string, message mailclient.Message) error {
	name := maildirName(message.UID)
	directory := "new"
	suffix := ""
	if hasFlag(message.Flags, `\Seen`) {
		directory = "cur"
		suffix = ":2," + maildirFlags(message.Flags)
	}
	tmpPath := filepath.Join(root, "tmp", name)
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create maildir message: %w", err)
	}
	if _, err := file.Write(message.Raw); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(root, directory, name+suffix)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("deliver maildir message: %w", err)
	}
	return nil
}

func maildirName(uid imap.UID) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	host = strings.NewReplacer("/", "_", `\`, "_", ":", "_", " ", "_").Replace(host)
	var random [4]byte
	_, _ = rand.Read(random[:])
	now := time.Now()
	return fmt.Sprintf("%d.M%dP%dQ%d.%x.%s", now.Unix(), now.Nanosecond()/1000, os.Getpid(), uid, random, host)
}

func maildirFlags(flags []string) string {
	set := make(map[byte]struct{}, 5)
	for _, flag := range flags {
		switch strings.ToLower(flag) {
		case `\answered`:
			set['R'] = struct{}{}
		case `\flagged`:
			set['F'] = struct{}{}
		case `\deleted`:
			set['T'] = struct{}{}
		case `\draft`:
			set['D'] = struct{}{}
		case `\seen`:
			set['S'] = struct{}{}
		}
	}
	letters := make([]byte, 0, len(set))
	for letter := range set {
		letters = append(letters, letter)
	}
	sort.Slice(letters, func(i, j int) bool { return letters[i] < letters[j] })
	return string(letters)
}

func hasFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, want) {
			return true
		}
	}
	return false
}
