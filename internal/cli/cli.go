// Package cli implements the perch command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/mxraven-go/mail/notify"
	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/imapd"
	"github.com/synqronlabs/perch/internal/ingest"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/provision"
	"github.com/synqronlabs/perch/internal/smtpd"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/perch/internal/tlsutil"
	"github.com/synqronlabs/raven/client"
	"golang.org/x/term"
)

// Version is the user-visible build version.
const Version = "0.1.0-dev"

// Run executes a subcommand and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	var err error
	switch args[0] {
	case "init":
		err = runInit(args[1:])
	case "setup":
		err = runSetup(args[1:])
	case "serve":
		err = runServe(args[1:])
	case "account":
		err = runAccount(args[1:])
	case "alias":
		err = runAlias(args[1:])
	case "mailbox":
		err = runMailbox(args[1:])
	case "doctor":
		err = runDoctor(args[1:])
	case "backup":
		err = runBackup(args[1:])
	case "version", "--version", "-version":
		fmt.Println("perch", Version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "perch:", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `perch - a small IMAP mailbox service backed by mxRaven

Usage:
  perch init       create a local configuration and optional first account
  perch setup      create the mxRaven domain, listeners, keys, and routes
  perch serve      run IMAP, submission, and the notification consumer
  perch account    add, list, password, disable, or enable accounts
  perch alias      manage address aliases and catch-all domains
  perch mailbox    list mailboxes for an account
  perch doctor     check the database, TLS, S3, and mxRaven connectivity
  perch backup     write a consistent SQLite backup
  perch version    print the version

Run "perch <command> -h" for command flags.
`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.Log)
	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("close database", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var s3 *blob.S3
	if cfg.Storage.Mode == config.StorageS3 {
		s3, err = blob.NewS3(ctx, cfg.Storage.S3)
		if err != nil {
			return err
		}
	}
	tlsConfig, acmeManager, err := tlsutil.Load(cfg.TLS, cfg.DataDir)
	if err != nil {
		return err
	}
	h := hub.New()
	ingester := ingest.New(cfg, st, s3, h, log)
	imapBackend := imapd.New(cfg, st, h, s3, log)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		imaps  []*imapserver.Server
		runErr error
	)
	fail := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if runErr == nil {
			runErr = err
		}
		mu.Unlock()
		stop()
	}

	if acmeManager != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tlsutil.RunACMEHTTP(ctx, acmeManager, cfg.TLS.ACME.HTTPAddr); err != nil {
				fail(fmt.Errorf("ACME HTTP listener: %w", err))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := ingester.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fail(fmt.Errorf("notification consumer: %w", err))
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := smtpd.New(cfg, st, log).Run(ctx, tlsConfig); err != nil && !errors.Is(err, context.Canceled) {
			fail(fmt.Errorf("submission server: %w", err))
		}
	}()

	if cfg.IMAP.Addr != "" {
		insecure := cfg.IMAP.AllowPlaintextLoopback && isLoopback(cfg.IMAP.Addr)
		if tlsConfig == nil && !insecure {
			return fmt.Errorf("IMAP listener %s requires TLS or a loopback bind", cfg.IMAP.Addr)
		}
		srv := imapserver.New(imapBackend.Options(tlsConfig, insecure))
		imaps = append(imaps, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("IMAP listener", "addr", cfg.IMAP.Addr, "starttls", tlsConfig != nil)
			if err := srv.ListenAndServe(cfg.IMAP.Addr); err != nil && ctx.Err() == nil {
				fail(fmt.Errorf("IMAP listener: %w", err))
			}
		}()
	}
	if cfg.IMAP.IMAPSAddr != "" {
		if tlsConfig == nil {
			return fmt.Errorf("IMAPS listener %s requires tls.cert_file and tls.key_file", cfg.IMAP.IMAPSAddr)
		}
		srv := imapserver.New(imapBackend.Options(tlsConfig, false))
		imaps = append(imaps, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("IMAPS listener", "addr", cfg.IMAP.IMAPSAddr)
			if err := srv.ListenAndServeTLS(cfg.IMAP.IMAPSAddr); err != nil && ctx.Err() == nil {
				fail(fmt.Errorf("IMAPS listener: %w", err))
			}
		}()
	}

	<-ctx.Done()
	for _, srv := range imaps {
		_ = srv.Close()
	}
	wait := make(chan struct{})
	go func() { wg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(5 * time.Second):
		log.Warn("shutdown timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	return runErr
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	dataDir := fs.String("data-dir", "", "data directory")
	hostname := fs.String("hostname", "localhost", "server hostname")
	email := fs.String("email", "", "first account email")
	name := fs.String("name", "", "first account display name")
	passwordValue := fs.String("password", "", "first account password (prompted when empty)")
	selfSigned := fs.Bool("self-signed", false, "generate a self-signed TLS certificate")
	force := fs.Bool("force", false, "overwrite an existing configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := *dataDir
	if dir == "" {
		dir = config.Default().DataDir
	}
	path := *configPath
	if path == "" {
		path = filepath.Join(dir, "perch.yaml")
	}
	if !*force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("configuration %q already exists (use --force)", path)
		}
	}
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Hostname = *hostname
	if *selfSigned {
		cert := filepath.Join(dir, "tls", "cert.pem")
		key := filepath.Join(dir, "tls", "key.pem")
		if err := tlsutil.GenerateSelfSigned(cert, key, *hostname); err != nil {
			return err
		}
		cfg.TLS.CertFile = cert
		cfg.TLS.KeyFile = key
		cfg.IMAP.IMAPSAddr = "127.0.0.1:1993"
		cfg.SMTP.TLSAddr = "127.0.0.1:1465"
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	if *email == "" {
		fmt.Printf("next: perch account add --config %s --email you@example.com\n", path)
		return nil
	}
	if *passwordValue == "" {
		value, err := promptPassword("Password: ")
		if err != nil {
			return err
		}
		*passwordValue = value
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	hash, err := password.Hash(*passwordValue)
	if err != nil {
		return err
	}
	account, err := st.CreateAccount(context.Background(), *email, *name, hash)
	if err != nil {
		return err
	}
	fmt.Printf("created account %s (id %d)\n", account.PrimaryAddress, account.ID)
	fmt.Printf("next: perch serve --config %s\n", path)
	return nil
}

func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	dataDir := fs.String("data-dir", "", "data directory")
	hostname := fs.String("hostname", "", "server hostname")
	token := fs.String("token", os.Getenv("MXRAVEN_API_TOKEN"), "control-plane bearer token")
	baseURL := fs.String("base-url", "", "control-plane base URL")
	workspace := fs.String("workspace", "", "mxRaven workspace slug")
	domain := fs.String("domain", "", "mail domain")
	inboundDomain := fs.String("inbound-domain", "", "inbound domain (defaults to --domain)")
	sendingDomain := fs.String("sending-domain", "", "sending domain (defaults to --domain)")
	noSubmission := fs.Bool("no-submission", false, "do not create a submission listener")
	dryRun := fs.Bool("dry-run", false, "print planned operations without calling mxRaven")
	force := fs.Bool("force", false, "overwrite an existing configuration")
	storage := fs.String("storage", "sqlite", "storage mode: sqlite or s3")
	s3Endpoint := fs.String("s3-endpoint", "", "S3 endpoint URL")
	s3Region := fs.String("s3-region", "", "S3 region")
	s3Bucket := fs.String("s3-bucket", "", "S3 bucket")
	s3Prefix := fs.String("s3-prefix", "perch/", "S3 key prefix")
	s3Access := fs.String("s3-access-key", os.Getenv("PERCH_S3_ACCESS_KEY"), "S3 access key")
	s3Secret := fs.String("s3-secret-key", os.Getenv("PERCH_S3_SECRET_KEY"), "S3 secret key")
	s3PathStyle := fs.Bool("s3-path-style", false, "use path-style S3 addressing")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	acmeEmail := fs.String("acme-email", "", "Let's Encrypt account email")
	acmeDomains := fs.String("acme-domain", "", "comma-separated ACME domains")
	public := fs.Bool("public", false, "bind standard public ports (143/993/587/465)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *domain == "" || *workspace == "" {
		return errors.New("--domain and --workspace are required")
	}
	if *token == "" {
		return errors.New("--token or MXRAVEN_API_TOKEN is required")
	}
	dir := *dataDir
	if dir == "" {
		dir = config.Default().DataDir
	}
	path := *configPath
	if path == "" {
		path = filepath.Join(dir, "perch.yaml")
	}
	cfg := config.Default()
	if existing, err := config.Load(path); err == nil {
		cfg = existing
	} else if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) {
		// A malformed existing config should not be silently overwritten.
		if !*force {
			return err
		}
	}
	cfg.DataDir = dir
	if *hostname != "" {
		cfg.Hostname = *hostname
	}
	if cfg.Hostname == "localhost" && *domain != "" {
		cfg.Hostname = "mail." + *domain
	}
	if *tlsCert != "" || *tlsKey != "" {
		if *tlsCert == "" || *tlsKey == "" {
			return errors.New("--tls-cert and --tls-key must be set together")
		}
		cfg.TLS.CertFile = *tlsCert
		cfg.TLS.KeyFile = *tlsKey
	}
	if *acmeEmail != "" || *acmeDomains != "" {
		if *acmeEmail == "" || *acmeDomains == "" {
			return errors.New("--acme-email and --acme-domain must be set together")
		}
		cfg.TLS.ACME.Enabled = true
		cfg.TLS.ACME.Email = *acmeEmail
		for _, domain := range strings.Split(*acmeDomains, ",") {
			if domain = strings.TrimSpace(domain); domain != "" {
				cfg.TLS.ACME.Domains = append(cfg.TLS.ACME.Domains, domain)
			}
		}
	}
	if *public {
		if cfg.TLS.CertFile == "" && !cfg.TLS.ACME.Enabled {
			return errors.New("--public requires --tls-cert/--tls-key or --acme-email/--acme-domain")
		}
		cfg.IMAP.Addr = ":143"
		cfg.IMAP.IMAPSAddr = ":993"
		cfg.IMAP.AllowPlaintextLoopback = false
		cfg.SMTP.Addr = ":587"
		cfg.SMTP.TLSAddr = ":465"
	}
	cfg.Storage.Mode = config.StorageMode(strings.ToLower(*storage))
	var err error
	if cfg.Storage.Mode == config.StorageS3 {
		if *s3Bucket == "" {
			return errors.New("--s3-bucket is required in s3 mode")
		}
		if *s3Access == "" {
			return errors.New("--s3-access-key or PERCH_S3_ACCESS_KEY is required in s3 mode")
		}
		if *s3Secret == "" {
			*s3Secret, err = promptPassword("S3 secret key: ")
			if err != nil {
				return err
			}
		}
	}
	if cfg.Storage.Mode == config.StorageS3 {
		prefix := strings.Trim(*s3Prefix, "/")
		if prefix != "" {
			prefix += "/"
		}
		cfg.Storage.S3 = config.S3Config{
			Endpoint: *s3Endpoint, Region: *s3Region, Bucket: *s3Bucket,
			Prefix: prefix, AccessKey: *s3Access, SecretKey: *s3Secret,
			ForcePathStyle: *s3PathStyle,
			KeyTemplate:    prefix + "{{.recipient.local}}/{{.recipient.domain}}/{{.task.id}}.eml",
			ReconcileEvery: 5 * time.Minute, Spool: true,
		}
	}
	result, err := provision.Run(context.Background(), provision.Options{
		BaseURL: *baseURL, Token: *token, Tenant: *workspace, Hostname: cfg.Hostname,
		DataDir: dir, Domain: *domain, InboundDomain: *inboundDomain,
		SendingDomain: *sendingDomain, EnableSubmission: !*noSubmission,
		Storage: cfg.Storage, DryRun: *dryRun,
	})
	if err != nil {
		return err
	}
	if *dryRun {
		for _, note := range result.Notes {
			fmt.Println("would:", note)
		}
		return nil
	}
	cfg.Storage = result.Config.Storage
	cfg.MXRaVen = result.Config.MXRaVen
	cfg.Setup = result.Config.Setup
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\n", path)
	if len(result.DNS) > 0 {
		fmt.Println("Publish these DNS records:")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "TYPE\tNAME\tVALUE\tPRIORITY")
		for _, record := range result.DNS {
			priority := ""
			if record.Priority > 0 {
				priority = fmt.Sprint(record.Priority)
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", record.Type, record.Name, record.Value, priority)
		}
		_ = w.Flush()
		fmt.Println()
	}
	fmt.Printf("next: perch account add --config %s --email you@%s\n", path, *domain)
	fmt.Printf("then: perch serve --config %s\n", path)
	return nil
}

func runAccount(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: perch account add|list|passwd|disable|enable")
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("account add", flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		email := fs.String("email", "", "account email")
		name := fs.String("name", "", "display name")
		passwordValue := fs.String("password", "", "password (prompted when empty)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *email == "" {
			return errors.New("--email is required")
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		if *passwordValue == "" {
			*passwordValue, err = promptPassword("Password: ")
			if err != nil {
				return err
			}
		}
		hash, err := password.Hash(*passwordValue)
		if err != nil {
			return err
		}
		account, err := st.CreateAccount(context.Background(), *email, *name, hash)
		if err != nil {
			return err
		}
		fmt.Printf("created %s (id %d)\n", account.PrimaryAddress, account.ID)
		return nil
	case "list":
		fs := flag.NewFlagSet("account list", flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		accounts, err := st.ListAccounts(context.Background())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tADDRESS\tNAME\tDISABLED\tCREATED")
		for _, account := range accounts {
			_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%t\t%s\n", account.ID, account.PrimaryAddress, account.DisplayName, account.Disabled, account.CreatedAt.Format(time.RFC3339))
		}
		return w.Flush()
	case "passwd":
		fs := flag.NewFlagSet("account passwd", flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		email := fs.String("email", "", "account email")
		passwordValue := fs.String("password", "", "new password (prompted when empty)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *email == "" {
			return errors.New("--email is required")
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		account, err := st.AccountByAddress(context.Background(), *email)
		if err != nil {
			return err
		}
		if *passwordValue == "" {
			*passwordValue, err = promptPassword("New password: ")
			if err != nil {
				return err
			}
		}
		hash, err := password.Hash(*passwordValue)
		if err != nil {
			return err
		}
		if err := st.SetPasswordHash(context.Background(), account.ID, hash); err != nil {
			return err
		}
		fmt.Printf("updated password for %s\n", account.PrimaryAddress)
		return nil
	case "disable", "enable":
		fs := flag.NewFlagSet("account "+args[0], flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		email := fs.String("email", "", "account email")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *email == "" {
			return errors.New("--email is required")
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		account, err := st.AccountByAddress(context.Background(), *email)
		if err != nil {
			return err
		}
		if err := st.SetAccountDisabled(context.Background(), account.ID, args[0] == "disable"); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", args[0]+"d", account.PrimaryAddress)
		return nil
	default:
		return fmt.Errorf("unknown account command %q", args[0])
	}
}

func runAlias(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: perch alias add|remove|list")
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("alias add", flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		accountEmail := fs.String("account", "", "account email")
		address := fs.String("address", "", "alias address or domain")
		catchAll := fs.Bool("catch-all", false, "route all unmatched addresses at --address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *accountEmail == "" || *address == "" {
			return errors.New("--account and --address are required")
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		account, err := st.AccountByAddress(context.Background(), *accountEmail)
		if err != nil {
			return err
		}
		if *catchAll {
			err = st.AddCatchAll(context.Background(), account.ID, *address)
		} else {
			err = st.AddAlias(context.Background(), account.ID, *address)
		}
		if err != nil {
			return err
		}
		fmt.Printf("added %s to %s\n", *address, account.PrimaryAddress)
		return nil
	case "remove":
		fs := flag.NewFlagSet("alias remove", flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		address := fs.String("address", "", "alias address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *address == "" {
			return errors.New("--address is required")
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		if err := st.RemoveAddress(context.Background(), *address); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", *address)
		return nil
	case "list":
		fs := flag.NewFlagSet("alias list", flag.ContinueOnError)
		configPath := fs.String("config", defaultConfigPath(), "configuration file")
		accountEmail := fs.String("account", "", "account email")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *accountEmail == "" {
			return errors.New("--account is required")
		}
		_, st, err := openConfigStore(*configPath)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		account, err := st.AccountByAddress(context.Background(), *accountEmail)
		if err != nil {
			return err
		}
		addresses, err := st.ListAddresses(context.Background(), account.ID)
		if err != nil {
			return err
		}
		for _, address := range addresses {
			fmt.Println(address)
		}
		return nil
	default:
		return fmt.Errorf("unknown alias command %q", args[0])
	}
}

func runMailbox(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: perch mailbox list --account you@example.com")
	}
	fs := flag.NewFlagSet("mailbox list", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "configuration file")
	accountEmail := fs.String("account", "", "account email")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *accountEmail == "" {
		return errors.New("--account is required")
	}
	_, st, err := openConfigStore(*configPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	account, err := st.AccountByAddress(context.Background(), *accountEmail)
	if err != nil {
		return err
	}
	mailboxes, err := st.ListMailboxes(context.Background(), account.ID)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSPECIAL-USE\tSUBSCRIBED")
	for _, mailbox := range mailboxes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%t\n", mailbox.Name, mailbox.SpecialUse, mailbox.Subscribed)
	}
	return w.Flush()
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("config: %s\n", *configPath)
	if info, err := os.Stat(*configPath); err == nil && info.Mode().Perm()&0o077 != 0 {
		fmt.Println("warning: configuration file is readable by group or others")
	}
	fmt.Printf("data:   %s\n", cfg.DataDir)
	fmt.Printf("mode:   %s\n", cfg.Storage.Mode)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.IntegrityCheck(ctx); err != nil {
		return err
	}
	fmt.Println("database: ok")
	ftsCount, err := st.CountIndexed(ctx)
	if err != nil {
		return fmt.Errorf("FTS5 check: %w", err)
	}
	fmt.Printf("FTS5: ok (%d indexed messages)\n", ftsCount)
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	dead, err := st.CountDeadLetters(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("accounts: %d; dead letters: %d\n", len(accounts), dead)
	if cfg.TLS.CertFile != "" {
		expiry, err := tlsutil.CertificateExpiry(cfg.TLS.CertFile)
		if err != nil {
			return err
		}
		fmt.Printf("TLS certificate expires: %s\n", expiry.Format(time.RFC3339))
	} else if cfg.TLS.ACME.Enabled {
		fmt.Println("TLS: ACME enabled")
	} else {
		fmt.Println("TLS: not configured (loopback plaintext only)")
	}
	if cfg.Storage.Mode == config.StorageS3 {
		s3, err := blob.NewS3(ctx, cfg.Storage.S3)
		if err != nil {
			return err
		}
		if err := s3.Ping(ctx); err != nil {
			return err
		}
		fmt.Printf("S3: ok (%s)\n", cfg.Storage.S3.Bucket)
	}
	for _, listener := range cfg.MXRaVen.Listeners {
		if listener.Kind != "mta" {
			continue
		}
		client, err := notify.New(
			notify.WithURL(cfg.MXRaVen.NotificationURL),
			notify.WithCredentials(listener.KeyPrefix, listener.Secret()),
			notify.WithListener(listener.ID),
		)
		if err != nil {
			return fmt.Errorf("mxRaven notification listener %s: %w", listener.ID, err)
		}
		_ = client.Close()
		fmt.Printf("mxRaven MTA listener %s: ok\n", listener.ID)
	}
	if submission, ok := cfg.Listener("submission"); ok {
		dialer := client.NewDialer(cfg.MXRaVen.SubmissionHost, cfg.MXRaVen.SubmissionPort)
		dialer.StartTLS = true
		dialer.RequireTLS = true
		dialer.LocalName = cfg.Hostname
		dialer.ConnectTimeout = 15 * time.Second
		dialer.Auth = &client.ClientAuth{Username: submission.KeyPrefix, Password: submission.Secret()}
		conn, err := dialer.DialContext(ctx)
		if err != nil {
			return fmt.Errorf("mxRaven submission: %w", err)
		}
		_ = conn.Quit()
		fmt.Println("mxRaven submission: ok")
	}
	return nil
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: perch backup [flags] <destination.db>")
	}
	_, st, err := openConfigStore(*configPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := st.Backup(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Printf("backup written to %s\n", fs.Arg(0))
	return nil
}

func openConfigStore(path string) (config.Config, *store.Store, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, st, nil
}

func defaultConfigPath() string {
	if path := os.Getenv("PERCH_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(config.Default().DataDir, "perch.yaml")
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.Format, "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func promptPassword(label string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, label)
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
	reader := bufio.NewReader(os.Stdin)
	value, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(value, "\r\n"), nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
