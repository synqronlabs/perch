// Package imapd implements perch's IMAP server on top of go-imap/v2.
package imapd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	mxfeedback "github.com/synqronlabs/mxraven-go/mail/feedback"
	"github.com/synqronlabs/perch/internal/authlimit"
	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/blobcache"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/store"
)

// Server holds the shared IMAP backend state.
type Server struct {
	cfg         config.Config
	store       *store.Store
	hub         *hub.Hub
	s3          *blob.S3
	log         *slog.Logger
	login       *authlimit.Limiter
	cache       *blobcache.Cache
	feedback    *mxfeedback.Client
	feedbackSem chan struct{}
}

// New creates an IMAP backend. s3 may be nil in SQLite mode.
func New(cfg config.Config, st *store.Store, h *hub.Hub, s3 *blob.S3, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	server := &Server{
		cfg: cfg, store: st, hub: h, s3: s3, log: log,
		login: authlimit.New(10, 5*time.Minute), cache: blobcache.New(cfg.Limits.BlobCacheBytes),
		feedbackSem: make(chan struct{}, 2),
	}
	if submission, ok := cfg.Listener("submission"); ok && cfg.MXRaVen.FeedbackURL != "" {
		if client, err := mxfeedback.New(
			mxfeedback.WithBaseURL(cfg.MXRaVen.FeedbackURL),
			mxfeedback.WithCredentials(submission.KeyPrefix, submission.Secret()),
		); err == nil {
			server.feedback = client
		} else {
			log.Warn("feedback client disabled", "error", err)
		}
	}
	return server
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
	cacheKey := m.RawSHA256
	if cacheKey == "" {
		cacheKey = fmt.Sprintf("message:%d", m.ID)
	}
	if raw, ok := s.cache.Get(cacheKey); ok {
		return raw, nil
	}
	raw, _, err := s.store.RawBlob(ctx, m.ID)
	if err == nil {
		s.cache.Put(cacheKey, raw)
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
	s.cache.Put(cacheKey, raw)
	return raw, nil
}

type imapLogger struct {
	log *slog.Logger
}

func (l imapLogger) Printf(format string, args ...any) {
	l.log.Warn(strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (s *Server) trainFeedback(ctx context.Context, disposition mxfeedback.Disposition, message store.Message) {
	if s.feedback == nil {
		return
	}
	select {
	case s.feedbackSem <- struct{}{}:
	default:
		s.log.Debug("feedback queue full", "message", message.ID)
		return
	}
	go func() {
		defer func() { <-s.feedbackSem }()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		raw, err := s.rawMessage(ctx, message)
		if err != nil {
			s.log.Warn("feedback raw message unavailable", "message", message.ID, "error", err)
			return
		}
		var result *mxfeedback.LearningResult
		switch disposition {
		case mxfeedback.DispositionSpam:
			result, err = s.feedback.LearnSpam(ctx, bytes.NewReader(raw))
		case mxfeedback.DispositionHam:
			result, err = s.feedback.LearnHam(ctx, bytes.NewReader(raw))
		}
		if err != nil {
			s.log.Warn("feedback submission failed", "message", message.ID, "disposition", disposition, "error", err)
			return
		}
		s.log.Info("feedback submitted", "message", message.ID, "disposition", result.Disposition, "matched", result.MatchedHashKind)
	}()
}

// trainMovedFeedback trains mxRaven for messages that were moved between the
// Junk mailbox and another mailbox.
func (s *Server) trainMovedFeedback(ctx context.Context, disposition mxfeedback.Disposition, mailboxID int64, uids []uint32) {
	if s.feedback == nil || len(uids) == 0 {
		return
	}
	messages, err := s.store.MessagesByUIDs(ctx, mailboxID, uids)
	if err != nil {
		s.log.Warn("feedback messages unavailable", "mailbox", mailboxID, "error", err)
		return
	}
	for _, message := range messages {
		s.trainFeedback(ctx, disposition, message)
	}
}
