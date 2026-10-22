package imapd

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
)

func startTestServer(t *testing.T) (addr string, st *store.Store, stop func()) {
	t.Helper()
	addr, st, _, stop = startTestServerWithBackend(t)
	return addr, st, stop
}

func startTestServerWithBackend(t *testing.T) (addr string, st *store.Store, backend *Server, stop func()) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Storage.Mode = config.StorageSQLite
	st, err := store.Open(filepath.Join(cfg.DataDir, "perch.db"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := password.Hash("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(context.Background(), "user@example.com", "User", hash); err != nil {
		t.Fatal(err)
	}
	backend = New(cfg, st, hub.New(), nil, slog.Default())
	srv := imapserver.New(backend.Options(nil, true))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), st, backend, func() {
		_ = srv.Close()
		_ = st.Close()
	}
}

func dialTest(t *testing.T, addr string) *imapclient.Client {
	t.Helper()
	client, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Login("user@example.com", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	return client
}

const testRaw = "From: Sender <sender@example.net>\r\n" +
	"To: user@example.com\r\n" +
	"Subject: Hello invoice\r\n" +
	"Date: Thu, 22 Oct 2026 03:00:00 +0000\r\n" +
	"Message-ID: <imap-test@example.net>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"This is a searchable invoice message.\r\n"

func TestIMAPLifecycle(t *testing.T) {
	addr, _, stop := startTestServer(t)
	defer stop()
	client := dialTest(t, addr)
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendCmd := client.Append("INBOX", int64(len(testRaw)), &imap.AppendOptions{Time: time.Now()})
	if _, err := appendCmd.Write([]byte(testRaw)); err != nil {
		t.Fatal(err)
	}
	if err := appendCmd.Close(); err != nil {
		t.Fatal(err)
	}
	appended, err := appendCmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if appended.UID == 0 {
		t.Fatal("append UID = 0")
	}

	search, err := client.Search(&imap.SearchCriteria{Text: []string{"invoice"}}, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	seqs := search.AllSeqNums()
	if len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("search seqs = %v", seqs)
	}
	shortSearch, err := client.Search(&imap.SearchCriteria{Text: []string{"in"}}, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(shortSearch.AllSeqNums()) != 1 {
		t.Fatalf("short search seqs = %v", shortSearch.AllSeqNums())
	}

	fetch := client.Fetch(imap.UIDSetNum(appended.UID), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true, RFC822Size: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection: []*imap.FetchItemBodySection{
			{}, {Specifier: imap.PartSpecifierHeader}, {Specifier: imap.PartSpecifierText},
			{Partial: &imap.SectionPartial{Offset: 0, Size: 10}},
		},
	})
	buffers, err := fetch.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(buffers) != 1 {
		t.Fatalf("fetch buffers = %d", len(buffers))
	}
	if buffers[0].Envelope.Subject != "Hello invoice" {
		t.Fatalf("subject = %q", buffers[0].Envelope.Subject)
	}
	if len(buffers[0].BodySection) != 4 || len(buffers[0].BodySection[0].Bytes) == 0 {
		t.Fatalf("body section = %+v", buffers[0].BodySection)
	}
	if !strings.Contains(string(buffers[0].BodySection[1].Bytes), "Subject:") {
		t.Fatalf("header section = %q", buffers[0].BodySection[1].Bytes)
	}
	if !strings.Contains(string(buffers[0].BodySection[2].Bytes), "searchable invoice") {
		t.Fatalf("text section = %q", buffers[0].BodySection[2].Bytes)
	}
	if len(buffers[0].BodySection[3].Bytes) != 10 {
		t.Fatalf("partial length = %d", len(buffers[0].BodySection[3].Bytes))
	}

	storeCmd := client.Store(imap.UIDSetNum(appended.UID), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged},
	}, nil)
	updated, err := storeCmd.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || !containsFlag(updated[0].Flags, imap.FlagFlagged) {
		t.Fatalf("updated flags = %+v", updated)
	}

	copyData, err := client.Copy(imap.UIDSetNum(appended.UID), "Archive").Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(copyData.DestUIDs) != 1 {
		t.Fatalf("copy data = %+v", copyData)
	}
	moveData, err := client.Move(imap.UIDSetNum(appended.UID), "Sent").Wait()
	if err != nil {
		t.Fatal(err)
	}
	moved, ok := moveData.DestUIDs.(imap.UIDSet)
	if !ok || len(moved) == 0 {
		t.Fatalf("move data = %+v", moveData)
	}
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := client.Logout().Wait(); err != nil {
		t.Fatal(err)
	}
}

func containsFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, flag := range flags {
		if flag == want {
			return true
		}
	}
	return false
}

func TestIdleDeliversExists(t *testing.T) {
	addr, _, stop := startTestServer(t)
	defer stop()
	rawConn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rawConn.Close() }()
	reader := bufio.NewReader(rawConn)
	readUntil(t, reader, "* OK")
	writeLine(t, rawConn, "a1 LOGIN user@example.com secret")
	readUntil(t, reader, "a1 OK")
	writeLine(t, rawConn, "a2 SELECT INBOX")
	readUntil(t, reader, "a2 OK")
	writeLine(t, rawConn, "a3 IDLE")
	readUntil(t, reader, "+")

	other := dialTest(t, addr)
	if _, err := other.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendCmd := other.Append("INBOX", int64(len(testRaw)), nil)
	if _, err := appendCmd.Write([]byte(testRaw)); err != nil {
		t.Fatal(err)
	}
	if err := appendCmd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := appendCmd.Wait(); err != nil {
		t.Fatal(err)
	}
	readUntil(t, reader, "* 1 EXISTS")
	writeLine(t, rawConn, "DONE")
	readUntil(t, reader, "a3 OK")
}

func readUntil(t *testing.T, reader *bufio.Reader, prefix string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, prefix) {
			return
		}
	}
	t.Fatalf("did not see %q", prefix)
}

func writeLine(t *testing.T, conn net.Conn, line string) {
	t.Helper()
	if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
		t.Fatal(err)
	}
}

func TestPollPropagatesFlagChanges(t *testing.T) {
	addr, _, stop := startTestServer(t)
	defer stop()
	first := dialTest(t, addr)
	if _, err := first.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	second := dialTest(t, addr)
	if _, err := second.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	appendCmd := second.Append("INBOX", int64(len(testRaw)), nil)
	if _, err := appendCmd.Write([]byte(testRaw)); err != nil {
		t.Fatal(err)
	}
	if err := appendCmd.Close(); err != nil {
		t.Fatal(err)
	}
	appended, err := appendCmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Store(imap.UIDSetNum(appended.UID), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged},
	}, nil).Collect(); err != nil {
		t.Fatal(err)
	}
	if err := first.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	fetch := first.Fetch(imap.UIDSetNum(appended.UID), &imap.FetchOptions{UID: true, Flags: true})
	buffers, err := fetch.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(buffers) != 1 || !containsFlag(buffers[0].Flags, imap.FlagFlagged) {
		t.Fatalf("flags = %+v", buffers)
	}
}
