package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/provision"
	cli "github.com/urfave/cli/v3"
)

func setupCommand() *cli.Command {
	return &cli.Command{
		Name:  "setup",
		Usage: "create the mxRaven domain, listeners, keys, and routes",
		Flags: []cli.Flag{
			configFlag(""),
			&cli.StringFlag{Name: "data-dir", Usage: "data directory"},
			&cli.StringFlag{Name: "hostname", Usage: "server hostname"},
			&cli.StringFlag{Name: "token", Value: os.Getenv("MXRAVEN_API_TOKEN"), Usage: "control-plane bearer token"},
			&cli.StringFlag{Name: "base-url", Usage: "control-plane base URL"},
			&cli.StringFlag{Name: "workspace", Usage: "mxRaven workspace slug"},
			&cli.StringFlag{Name: "domain", Usage: "mail domain"},
			&cli.StringFlag{Name: "inbound-domain", Usage: "inbound domain (defaults to --domain)"},
			&cli.StringFlag{Name: "sending-domain", Usage: "sending domain (defaults to --domain)"},
			&cli.BoolFlag{Name: "no-submission", Usage: "do not create a submission listener"},
			&cli.StringFlag{Name: "auth-policy", Value: "none", Usage: "MTA auth policy: none, reject-dmarc-fail, reject-spf-fail, reject-unauthenticated"},
			&cli.BoolFlag{Name: "rspamd", Value: true, Usage: "enable Rspamd scanning on the MTA listener"},
			&cli.BoolFlag{Name: "reject-malware", Value: true, Usage: "reject messages with malware verdicts"},
			&cli.BoolFlag{Name: "reject-high-confidence-spam", Usage: "reject messages with spam score >= 10"},
			&cli.Float64Flag{Name: "mta-message-rate", Usage: "MTA messages per minute (0 leaves default)"},
			&cli.Float64Flag{Name: "mta-recipient-rate", Usage: "MTA recipients per minute"},
			&cli.Float64Flag{Name: "mta-task-rate", Usage: "MTA tasks per minute"},
			&cli.IntFlag{Name: "mta-burst", Usage: "MTA rate-limit burst"},
			&cli.IntFlag{Name: "mta-concurrency", Usage: "MTA max concurrency"},
			&cli.BoolFlag{Name: "dry-run", Usage: "print planned operations without calling mxRaven"},
			&cli.BoolFlag{Name: "force", Usage: "overwrite an existing configuration"},
			&cli.BoolFlag{Name: "wait", Usage: "wait for mxRaven DNS verification after setup"},
			&cli.DurationFlag{Name: "wait-timeout", Value: 10 * time.Minute, Usage: "DNS verification timeout"},
			&cli.DurationFlag{Name: "wait-interval", Value: 15 * time.Second, Usage: "DNS verification poll interval"},
			&cli.StringFlag{Name: "storage", Value: "sqlite", Usage: "storage mode: sqlite or s3"},
			&cli.StringFlag{Name: "s3-endpoint", Usage: "S3 endpoint URL"},
			&cli.StringFlag{Name: "s3-region", Usage: "S3 region"},
			&cli.StringFlag{Name: "s3-bucket", Usage: "S3 bucket"},
			&cli.StringFlag{Name: "s3-prefix", Value: "perch/", Usage: "S3 key prefix"},
			&cli.StringFlag{Name: "s3-access-key", Value: os.Getenv("PERCH_S3_ACCESS_KEY"), Usage: "S3 access key"},
			&cli.StringFlag{Name: "s3-secret-key", Value: os.Getenv("PERCH_S3_SECRET_KEY"), Usage: "S3 secret key"},
			&cli.BoolFlag{Name: "s3-path-style", Usage: "use path-style S3 addressing"},
			&cli.StringFlag{Name: "s3-key-template", Usage: "S3 object key template (defaults to prefix + mailbox.address)"},
			&cli.StringFlag{Name: "tls-cert", Usage: "TLS certificate file"},
			&cli.StringFlag{Name: "tls-key", Usage: "TLS private key file"},
			&cli.StringFlag{Name: "acme-email", Usage: "Let's Encrypt account email"},
			&cli.StringFlag{Name: "acme-domain", Usage: "comma-separated ACME domains"},
			&cli.BoolFlag{Name: "public", Usage: "bind standard public ports (143/993/587/465)"},
		},
		Action: runSetup,
	}
}

func runSetup(ctx context.Context, cmd *cli.Command) error {
	domain := cmd.String("domain")
	workspace := cmd.String("workspace")
	if domain == "" || workspace == "" {
		return errors.New("--domain and --workspace are required")
	}
	authPolicy := cmd.String("auth-policy")
	switch strings.ToLower(authPolicy) {
	case "", "none", "reject-dmarc-fail", "reject-spf-fail", "reject-unauthenticated":
	default:
		return fmt.Errorf("unknown --auth-policy %q", authPolicy)
	}
	mtaMessageRate := cmd.Float64("mta-message-rate")
	mtaRecipientRate := cmd.Float64("mta-recipient-rate")
	mtaTaskRate := cmd.Float64("mta-task-rate")
	mtaBurst := cmd.Int("mta-burst")
	mtaConcurrency := cmd.Int("mta-concurrency")
	if mtaMessageRate < 0 || mtaRecipientRate < 0 || mtaTaskRate < 0 || mtaBurst < 0 || mtaConcurrency < 0 {
		return errors.New("MTA rate-limit values must not be negative")
	}
	if (mtaMessageRate > 0 || mtaRecipientRate > 0 || mtaTaskRate > 0 || mtaBurst > 0 || mtaConcurrency > 0) &&
		(mtaMessageRate <= 0 || mtaRecipientRate <= 0 || mtaTaskRate <= 0) {
		return errors.New("--mta-message-rate, --mta-recipient-rate, and --mta-task-rate must all be set together")
	}
	token := cmd.String("token")
	if token == "" {
		return errors.New("--token or MXRAVEN_API_TOKEN is required")
	}
	dir := cmd.String("data-dir")
	if dir == "" {
		dir = config.Default().DataDir
	}
	path := cmd.String("config")
	if path == "" {
		path = filepath.Join(dir, "perch.yaml")
	}
	cfg := config.Default()
	if existing, err := config.Load(path); err == nil {
		cfg = existing
	} else if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) {
		// A malformed existing config should not be silently overwritten.
		if !cmd.Bool("force") {
			return err
		}
	}
	cfg.DataDir = dir
	if hostname := cmd.String("hostname"); hostname != "" {
		cfg.Hostname = hostname
	}
	if cfg.Hostname == "localhost" && domain != "" {
		cfg.Hostname = "mail." + domain
	}
	tlsCert := cmd.String("tls-cert")
	tlsKey := cmd.String("tls-key")
	if tlsCert != "" || tlsKey != "" {
		if tlsCert == "" || tlsKey == "" {
			return errors.New("--tls-cert and --tls-key must be set together")
		}
		cfg.TLS.CertFile = tlsCert
		cfg.TLS.KeyFile = tlsKey
	}
	acmeEmail := cmd.String("acme-email")
	acmeDomains := cmd.String("acme-domain")
	if acmeEmail != "" || acmeDomains != "" {
		if acmeEmail == "" || acmeDomains == "" {
			return errors.New("--acme-email and --acme-domain must be set together")
		}
		cfg.TLS.ACME.Enabled = true
		cfg.TLS.ACME.Email = acmeEmail
		for _, value := range strings.Split(acmeDomains, ",") {
			if value = strings.TrimSpace(value); value != "" {
				cfg.TLS.ACME.Domains = append(cfg.TLS.ACME.Domains, value)
			}
		}
	}
	if cmd.Bool("public") {
		if cfg.TLS.CertFile == "" && !cfg.TLS.ACME.Enabled {
			return errors.New("--public requires --tls-cert/--tls-key or --acme-email/--acme-domain")
		}
		cfg.IMAP.Addr = ":143"
		cfg.IMAP.IMAPSAddr = ":993"
		cfg.IMAP.AllowPlaintextLoopback = false
		cfg.SMTP.Addr = ":587"
		cfg.SMTP.TLSAddr = ":465"
	}
	storage := cmd.String("storage")
	cfg.Storage.Mode = config.StorageMode(strings.ToLower(storage))
	if cfg.Storage.Mode != config.StorageSQLite && cfg.Storage.Mode != config.StorageS3 {
		return fmt.Errorf("unknown --storage %q", storage)
	}
	var err error
	s3Access := cmd.String("s3-access-key")
	s3Secret := cmd.String("s3-secret-key")
	if cfg.Storage.Mode == config.StorageS3 {
		if cmd.String("s3-bucket") == "" {
			return errors.New("--s3-bucket is required in s3 mode")
		}
		if s3Access == "" {
			return errors.New("--s3-access-key or PERCH_S3_ACCESS_KEY is required in s3 mode")
		}
		if s3Secret == "" {
			s3Secret, err = promptPassword("S3 secret key: ")
			if err != nil {
				return err
			}
		}
	}
	if cfg.Storage.Mode == config.StorageS3 {
		prefix := strings.Trim(cmd.String("s3-prefix"), "/")
		if prefix != "" {
			prefix += "/"
		}
		keyTemplate := cmd.String("s3-key-template")
		if keyTemplate == "" {
			keyTemplate = prefix + "{{.mailbox.address}}/{{.task.id}}.eml"
		}
		cfg.Storage.S3 = config.S3Config{
			Endpoint: cmd.String("s3-endpoint"), Region: cmd.String("s3-region"), Bucket: cmd.String("s3-bucket"),
			Prefix: prefix, AccessKey: s3Access, SecretKey: s3Secret,
			ForcePathStyle: cmd.Bool("s3-path-style"),
			KeyTemplate:    keyTemplate,
			ReconcileEvery: 5 * time.Minute, Spool: false, SpoolMaxBytes: 256 << 20,
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	var rateLimit *provision.RateLimit
	if mtaMessageRate > 0 || mtaRecipientRate > 0 || mtaTaskRate > 0 || mtaBurst > 0 || mtaConcurrency > 0 {
		burst := int32(mtaBurst)
		if burst == 0 {
			burst = 10
		}
		concurrency := int32(mtaConcurrency)
		if concurrency == 0 {
			concurrency = 5
		}
		rateLimit = &provision.RateLimit{
			MessageRatePerMinute: mtaMessageRate, RecipientRatePerMinute: mtaRecipientRate,
			TaskRatePerMinute: mtaTaskRate, Burst: burst, MaxConcurrency: concurrency,
		}
	}
	result, err := provision.Run(ctx, provision.Options{
		BaseURL: cmd.String("base-url"), Token: token, Tenant: workspace, Hostname: cfg.Hostname,
		DataDir: dir, Domain: domain, InboundDomain: cmd.String("inbound-domain"),
		SendingDomain: cmd.String("sending-domain"), EnableSubmission: !cmd.Bool("no-submission"),
		Storage: cfg.Storage, DryRun: cmd.Bool("dry-run"),
		AuthPolicy: authPolicy, Rspamd: cmd.Bool("rspamd"),
		RejectMalware: cmd.Bool("reject-malware"), RejectHighConfidenceSpam: cmd.Bool("reject-high-confidence-spam"),
		RateLimit: rateLimit,
	})
	if err != nil {
		return err
	}
	if cmd.Bool("dry-run") {
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
		rows := make([][]string, 0, len(result.DNS))
		for _, record := range result.DNS {
			priority := ""
			if record.Priority > 0 {
				priority = fmt.Sprint(record.Priority)
			}
			rows = append(rows, []string{record.Type, record.Name, record.Value, priority})
		}
		if err := printTable([]string{"TYPE", "NAME", "VALUE", "PRIORITY"}, rows); err != nil {
			return err
		}
		fmt.Println()
	}
	if cmd.Bool("wait") {
		if err := provision.WaitForDNS(ctx, cmd.String("base-url"), token, workspace, result,
			cmd.Duration("wait-interval"), cmd.Duration("wait-timeout"), func(format string, args ...any) {
				fmt.Printf(format+"\n", args...)
			}); err != nil {
			return err
		}
	}
	fmt.Printf("next: perch account add --config %s --email you@%s\n", path, domain)
	fmt.Printf("then: perch serve --config %s\n", path)
	return nil
}
