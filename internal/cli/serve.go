package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/imapd"
	"github.com/synqronlabs/perch/internal/ingest"
	"github.com/synqronlabs/perch/internal/netlimit"
	"github.com/synqronlabs/perch/internal/netutil"
	"github.com/synqronlabs/perch/internal/smtpd"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/perch/internal/tlsutil"
	cli "github.com/urfave/cli/v3"
)

func serveCommand() *cli.Command {
	return &cli.Command{
		Name:   "serve",
		Usage:  "run IMAP, submission, and the notification consumer",
		Flags:  []cli.Flag{configFlag(defaultConfigPath())},
		Action: runServe,
	}
}

func runServe(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.Load(cmd.String("config"))
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

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	var s3 *blob.S3
	if cfg.Storage.Mode == config.StorageS3 {
		s3, err = blob.NewS3(ctx, cfg.Storage.S3)
		if err != nil {
			return err
		}
	}
	connLimiter := netlimit.New(cfg.Limits.MaxConnections)
	defer connLimiter.Close()
	tlsConfig, acmeManager, tlsReloader, err := tlsutil.Load(cfg.TLS, cfg.DataDir)
	if err != nil {
		return err
	}
	if tlsReloader != nil {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-hup:
					if err := tlsReloader.Reload(); err != nil {
						log.Warn("TLS certificate reload failed", "error", err)
					} else {
						log.Info("TLS certificate reloaded")
					}
				}
			}
		}()
	}
	h := hub.New()
	ingester := ingest.New(cfg, st, s3, h, log)
	imapBackend := imapd.New(cfg, st, h, s3, log)

	var (
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
		if err := smtpd.New(cfg, st, log, connLimiter).Run(ctx, tlsConfig); err != nil && !errors.Is(err, context.Canceled) {
			fail(fmt.Errorf("submission server: %w", err))
		}
	}()

	if cfg.IMAP.Addr != "" {
		insecure := cfg.IMAP.AllowPlaintextLoopback && netutil.IsLoopback(cfg.IMAP.Addr)
		if tlsConfig == nil && !insecure {
			return fmt.Errorf("IMAP listener %s requires TLS or a loopback bind", cfg.IMAP.Addr)
		}
		ln, err := net.Listen("tcp", cfg.IMAP.Addr)
		if err != nil {
			return fmt.Errorf("listen on IMAP address %s: %w", cfg.IMAP.Addr, err)
		}
		ln = connLimiter.Wrap(ln)
		srv := imapserver.New(imapBackend.Options(tlsConfig, insecure))
		imaps = append(imaps, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("IMAP listener", "addr", cfg.IMAP.Addr, "starttls", tlsConfig != nil)
			if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
				fail(fmt.Errorf("IMAP listener: %w", err))
			}
		}()
	}
	if cfg.IMAP.IMAPSAddr != "" {
		if tlsConfig == nil {
			return fmt.Errorf("IMAPS listener %s requires tls.cert_file and tls.key_file", cfg.IMAP.IMAPSAddr)
		}
		ln, err := net.Listen("tcp", cfg.IMAP.IMAPSAddr)
		if err != nil {
			return fmt.Errorf("listen on IMAPS address %s: %w", cfg.IMAP.IMAPSAddr, err)
		}
		ln = tls.NewListener(connLimiter.Wrap(ln), tlsConfig)
		srv := imapserver.New(imapBackend.Options(tlsConfig, false))
		imaps = append(imaps, srv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Info("IMAPS listener", "addr", cfg.IMAP.IMAPSAddr)
			if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
				fail(fmt.Errorf("IMAPS listener: %w", err))
			}
		}()
	}

	<-ctx.Done()
	connLimiter.Close()
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
