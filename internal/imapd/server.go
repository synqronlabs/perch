// Package imapd implements perch's IMAP server on top of go-imap/v2.
package imapd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/synqronlabs/perch/internal/authlimit"
	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/store"
)

// Server holds the shared IMAP backend state.
type Server struct {
	cfg   config.Config
	store *store.Store
	hub   *hub.Hub
	s3    *blob.S3
	log   *slog.Logger
	login *authlimit.Limiter
}

// New creates an IMAP backend. s3 may be nil in SQLite mode.
func New(cfg config.Config, st *store.Store, h *hub.Hub, s3 *blob.S3, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, store: st, hub: h, s3: s3, log: log, login: authlimit.New(10, 5*time.Minute)}
}

// Options returns go-imap server options for one listener.
func (s *Server) Options(tlsConfig *tls.Config, insecureAuth bool) *imapserver.Options {
	return &imapserver.Options{
		NewSession:   s.newSession,
		Caps:         s.capabilities(),
		Logger:       imapLogger{log: s.log},
		TLSConfig:    tlsConfig,
		InsecureAuth: insecureAuth,
	}
}

func (s *Server) capabilities() imap.CapSet {
	caps := imap.CapSet{
		imap.CapIMAP4rev1:        {},
		imap.CapIdle:             {},
		imap.CapUIDPlus:          {},
		imap.CapMove:             {},
		imap.CapNamespace:        {},
		imap.CapUnselect:         {},
		imap.CapSpecialUse:       {},
		imap.CapCreateSpecialUse: {},
		imap.CapListExtended:     {},
		imap.CapListStatus:       {},
		imap.CapStatusSize:       {},
		imap.CapESearch:          {},
		imap.CapLiteralMinus:     {},
		imap.CapSASLIR:           {},
		imap.CapChildren:         {},
		imap.Cap(fmt.Sprintf("APPENDLIMIT=%d", s.cfg.Limits.MaxMessageBytes)): {},
	}
	caps[imap.AuthCap("PLAIN")] = struct{}{}
	caps[imap.AuthCap("LOGIN")] = struct{}{}
	return caps
}

func (s *Server) newSession(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
	return &session{srv: s, conn: conn}, &imapserver.GreetingData{}, nil
}

func (s *Server) rawMessage(ctx context.Context, m store.Message) ([]byte, error) {
	raw, _, err := s.store.RawBlob(ctx, m.ID)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, store.ErrBlobUnavailable) {
		return nil, err
	}
	if s.s3 == nil || m.S3Key == "" {
		return nil, fmt.Errorf("message body is unavailable")
	}
	raw, err = s.s3.Get(ctx, m.S3Key)
	if err != nil {
		return nil, fmt.Errorf("fetch message body from S3: %w", err)
	}
	return raw, nil
}

type imapLogger struct {
	log *slog.Logger
}

func (l imapLogger) Printf(format string, args ...any) {
	l.log.Warn(strings.TrimSpace(fmt.Sprintf(format, args...)))
}
