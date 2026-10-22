package cli

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/synqronlabs/mxraven-go/mail/notify"
	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/perch/internal/tlsutil"
	"github.com/synqronlabs/raven/client"
	cli "github.com/urfave/cli/v3"
)

func doctorCommand() *cli.Command {
	return &cli.Command{
		Name:   "doctor",
		Usage:  "check the database, TLS, S3, and mxRaven connectivity",
		Flags:  []cli.Flag{configFlag(defaultConfigPath())},
		Action: runDoctor,
	}
}

func runDoctor(ctx context.Context, cmd *cli.Command) error {
	configPath := cmd.String("config")
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	fmt.Printf("config: %s\n", configPath)
	if info, err := os.Stat(configPath); err == nil && info.Mode().Perm()&0o077 != 0 {
		fmt.Println("warning: configuration file is readable by group or others")
	}
	fmt.Printf("data:   %s\n", cfg.DataDir)
	fmt.Printf("mode:   %s\n", cfg.Storage.Mode)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
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
	outbound, err := st.CountOutbound(ctx)
	if err != nil {
		return err
	}
	if len(outbound) > 0 {
		parts := make([]string, 0, len(outbound))
		for status, count := range outbound {
			parts = append(parts, fmt.Sprintf("%s=%d", status, count))
		}
		sort.Strings(parts)
		fmt.Printf("outbound: %s\n", strings.Join(parts, ", "))
	}
	fmt.Printf("limits: max_connections=%d ingest_concurrency=%d blob_cache=%d bytes\n",
		cfg.Limits.MaxConnections, cfg.Limits.IngestConcurrency, cfg.Limits.BlobCacheBytes)
	if cfg.TLS.CertFile != "" {
		expiry, err := tlsutil.CertificateExpiry(cfg.TLS.CertFile)
		if err != nil {
			return err
		}
		fmt.Printf("TLS certificate expires: %s\n", expiry.Format(time.RFC3339))
		if time.Until(expiry) < 30*24*time.Hour {
			fmt.Println("warning: TLS certificate expires within 30 days")
		}
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
		objects, err := s3.List(ctx, "")
		if err != nil {
			return err
		}
		fmt.Printf("S3 objects under %q: %d\n", cfg.Storage.S3.Prefix, len(objects))
		spoolBytes, spoolCount, err := st.SpoolStats(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("S3 spool: %d objects, %d bytes (limit %d)\n", spoolCount, spoolBytes, cfg.Storage.S3.SpoolMaxBytes)
	}
	if domain := cmp.Or(cfg.Setup.InboundDomain, cfg.Setup.Domain); domain != "" {
		mx, err := net.LookupMX(domain)
		if err != nil || len(mx) == 0 {
			fmt.Printf("warning: no MX records found for %s\n", domain)
		} else {
			for _, record := range mx {
				fmt.Printf("MX %s -> %s (%d)\n", domain, record.Host, record.Pref)
			}
		}
		if txt, err := net.LookupTXT("_dmarc." + domain); err != nil || len(txt) == 0 {
			fmt.Printf("warning: no DMARC TXT record found for %s\n", domain)
		}
		for _, name := range []string{"mxr1._domainkey." + domain, "mxr2._domainkey." + domain, "bounces." + domain} {
			if _, err := net.LookupCNAME(name); err != nil {
				fmt.Printf("warning: no CNAME found for %s\n", name)
			}
		}
	}
	for _, listener := range cfg.MXRaVen.Listeners {
		notifyOpts := []notify.Option{
			notify.WithURL(cfg.MXRaVen.NotificationURL),
			notify.WithCredentials(listener.KeyPrefix, listener.Secret()),
			notify.WithListener(listener.ID),
		}
		if listener.Consumer != "" {
			notifyOpts = append(notifyOpts, notify.WithConsumer(listener.Consumer))
		}
		notifyClient, err := notify.New(notifyOpts...)
		if err != nil {
			return fmt.Errorf("mxRaven notification listener %s: %w", listener.ID, err)
		}
		_ = notifyClient.Close()
		fmt.Printf("mxRaven %s listener %s: ok\n", listener.Kind, listener.ID)
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
