// Package smtpd exposes authenticated submission and relays it to mxRaven
// with Raven's streaming SMTP client.
package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/synqronlabs/perch/internal/authlimit"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/raven/server"
)

// Server runs the submission listeners.
type Server struct {
	cfg   config.Config
	store *store.Store
	log   *slog.Logger
	login *authlimit.Limiter
}

// New creates a submission server.
func New(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, store: st, log: log, login: authlimit.New(10, 5*time.Minute)}
}

// Run starts the configured submission listeners and blocks until ctx is
// cancelled or a listener fails.
func (s *Server) Run(ctx context.Context, tlsConfig *tls.Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type listenerSpec struct {
		addr string
		tls  bool
	}
	specs := []listenerSpec{
		{addr: s.cfg.SMTP.Addr},
		{addr: s.cfg.SMTP.TLSAddr, tls: true},
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(specs))
	started := 0
	for _, spec := range specs {
		if spec.addr == "" {
			continue
		}
		if tlsConfig == nil && (spec.tls || !isLoopback(spec.addr)) {
			return fmt.Errorf("submission listener %s requires TLS", spec.addr)
		}
		started++
		srv := server.NewServer(backend{s: s}, server.ServerConfig{
			Domain:             s.cfg.Hostname,
			Addr:               spec.addr,
			TLSConfig:          tlsConfig,
			AllowInsecureAuth:  !spec.tls && isLoopback(spec.addr),
			MaxMessageBytes:    s.cfg.Limits.MaxMessageBytes,
			MaxRecipients:      s.cfg.Limits.MaxRecipients,
			MaxReceivedHeaders: 3,
			EnableSMTPUTF8:     true,
			ReadTimeout:        5 * time.Minute,
			WriteTimeout:       5 * time.Minute,
			DataTimeout:        10 * time.Minute,
			ErrorLog:           smtpLogger{log: s.log},
		})
		wg.Add(1)
		go func(spec listenerSpec, srv *server.Server) {
			defer wg.Done()
			var err error
			if spec.tls {
				err = srv.ListenAndServeTLS(ctx)
			} else {
				err = srv.ListenAndServe(ctx)
			}
			if err != nil && !errors.Is(err, server.ErrServerClosed) && ctx.Err() == nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}(spec, srv)
	}
	if started == 0 {
		return nil
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

type backend struct {
	s *Server
}

func (b backend) NewSession(conn *server.Conn) (server.Session, error) {
	return &session{srv: b.s, conn: conn}, nil
}

type smtpLogger struct {
	log *slog.Logger
}

func (l smtpLogger) Printf(format string, args ...any) {
	l.log.Warn(strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l smtpLogger) Println(args ...any) {
	l.log.Warn(strings.TrimSpace(fmt.Sprint(args...)))
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
