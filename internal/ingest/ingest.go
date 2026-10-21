// Package ingest consumes mxRaven notification events and stores inbound mail.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/synqronlabs/mxraven-go/mail/notify"
	"github.com/synqronlabs/mxraven-go/mail/webhook"
	"github.com/synqronlabs/perch/internal/blob"
	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/hub"
	"github.com/synqronlabs/perch/internal/mailparse"
	"github.com/synqronlabs/perch/internal/store"
)

// Ingester consumes the MTA listener's notification stream and, in S3 mode,
// reconciles the mxRaven archive bucket.
type Ingester struct {
	cfg    config.Config
	store  *store.Store
	s3     *blob.S3
	hub    *hub.Hub
	log    *slog.Logger
	client *http.Client
	sem    chan struct{}
}

// New creates an ingester. s3 may be nil in SQLite mode.
func New(cfg config.Config, st *store.Store, s3 *blob.S3, h *hub.Hub, log *slog.Logger) *Ingester {
	if log == nil {
		log = slog.Default()
	}
	concurrency := cfg.Limits.IngestConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	return &Ingester{
		cfg: cfg, store: st, s3: s3, hub: h, log: log,
		client: &http.Client{Timeout: 60 * time.Second},
		sem:    make(chan struct{}, concurrency),
	}
}

// Run consumes all configured MTA listeners until ctx is cancelled and runs
// the S3 reconciler when configured. It returns the first terminal error.
func (i *Ingester) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if i.cfg.Storage.Mode == config.StorageS3 && i.s3 != nil {
		go i.reconcileLoop(ctx)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(i.cfg.MXRaVen.Listeners))
	started := 0
	for _, listener := range i.cfg.MXRaVen.Listeners {
		started++
		wg.Add(1)
		go func(listener config.ListenerConfig) {
			defer wg.Done()
			if err := i.consume(ctx, listener); err != nil && !errors.Is(err, context.Canceled) {
				if listener.Kind == "submission" {
					i.log.Warn("submission notification consumer stopped", "listener", listener.ID, "error", err)
					return
				}
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}(listener)
	}
	if started == 0 {
		i.log.Warn("no mxRaven listeners configured; inbound and submission events will not be consumed")
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

func (i *Ingester) consume(ctx context.Context, listener config.ListenerConfig) error {
	opts := []notify.Option{
		notify.WithURL(i.cfg.MXRaVen.NotificationURL),
		notify.WithCredentials(listener.KeyPrefix, listener.Secret()),
		notify.WithListener(listener.ID),
		notify.WithErrorHandler(func(err error) {
			i.log.Warn("notification stream error", "listener", listener.ID, "error", err)
		}),
	}
	if listener.Consumer != "" {
		opts = append(opts, notify.WithConsumer(listener.Consumer))
	}
	client, err := notify.New(opts...)
	if err != nil {
		return fmt.Errorf("connect notification listener %s: %w", listener.ID, err)
	}
	defer func() { _ = client.Close() }()
	i.log.Info("consuming mxRaven notifications", "listener", listener.ID, "kind", listener.Kind)
	if err := client.Consume(ctx, func(ctx context.Context, event notify.Event) error {
		return i.handleEvent(ctx, listener.Kind, event)
	}); err != nil {
		return fmt.Errorf("consume notification listener %s: %w", listener.ID, err)
	}
	return nil
}

func (i *Ingester) handleEvent(ctx context.Context, kind string, event notify.Event) error {
	select {
	case i.sem <- struct{}{}:
		defer func() { <-i.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	switch {
	case event.Accepted != nil && kind == "mta":
		return i.handleAccepted(ctx, event.Accepted)
	case event.Accepted != nil && kind == "submission":
		return i.handleSubmissionAccepted(ctx, event.Accepted)
	case event.Attempt != nil && kind == "submission":
		return i.handleSubmissionAttempt(ctx, event.Attempt)
	case event.Attempt != nil && kind == "mta":
		return i.handleAttempt(ctx, event.Attempt)
	default:
		return nil
	}
}

func (i *Ingester) handleSubmissionAccepted(ctx context.Context, payload *webhook.InboundEmail) error {
	if payload == nil || (payload.MessageRef == "" && payload.TaskID == "") {
		return nil
	}
	var accountID int64
	if account, err := i.store.AccountByAddress(ctx, payload.Envelope.MailFrom); err == nil {
		accountID = account.ID
	}
	return i.store.UpsertOutboundAccepted(ctx, store.Outbound{
		AccountID: accountID, MessageRef: payload.MessageRef, TaskID: payload.TaskID,
		FromAddress: payload.Envelope.MailFrom, Recipients: payload.Envelope.RcptTo,
		Subject: payload.Message.Subject, Status: "accepted",
		AcceptedAt: time.Unix(payload.AcceptedAtUTC, 0).UTC(),
		OccurredAt: time.Unix(payload.OccurredAtUTC, 0).UTC(),
	})
}

func (i *Ingester) handleSubmissionAttempt(ctx context.Context, attempt *notify.AttemptEvent) error {
	if attempt == nil || (attempt.MessageRef == "" && attempt.TaskID == "") {
		return nil
	}
	var acceptedAt, occurredAt, nextRetry time.Time
	if attempt.AcceptedAtUTC > 0 {
		acceptedAt = time.Unix(attempt.AcceptedAtUTC, 0).UTC()
	}
	if attempt.OccurredAtUTC > 0 {
		occurredAt = time.Unix(attempt.OccurredAtUTC, 0).UTC()
	}
	if attempt.NextRetryAtUTC > 0 {
		nextRetry = time.Unix(attempt.NextRetryAtUTC, 0).UTC()
	}
	return i.store.RecordOutboundAttempt(ctx, store.Outbound{
		MessageRef: attempt.MessageRef, TaskID: attempt.TaskID,
		Status: string(attempt.Status), Attempt: attempt.Attempt,
		SMTPCode: attempt.SMTPCode, EnhancedStatus: attempt.EnhancedStatusCode,
		RemoteResponse: attempt.RemoteResponse, Reason: attempt.Reason,
		AcceptedAt: acceptedAt, OccurredAt: occurredAt, NextRetryAt: nextRetry,
	})
}

func (i *Ingester) handleAccepted(ctx context.Context, payload *webhook.InboundEmail) error {
	if payload == nil {
		return nil
	}
	if _, err := i.store.MessageByTaskID(ctx, payload.TaskID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if len(payload.Envelope.RcptTo) == 0 {
		i.log.Warn("accepted event has no recipients", "task", payload.TaskID)
		return nil
	}
	raw, err := payload.RawEmail.Fetch(ctx, i.client)
	if err != nil {
		return fmt.Errorf("fetch raw email for task %s: %w", payload.TaskID, err)
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse raw email for task %s: %w", payload.TaskID, err)
	}

	var account store.Account
	var matched string
	for _, recipient := range payload.Envelope.RcptTo {
		account, matched, err = i.store.ResolveRecipient(ctx, recipient)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrNoRecipient) && !errors.Is(err, store.ErrAccountDisabled) {
			return fmt.Errorf("resolve recipient %q: %w", recipient, err)
		}
	}
	if matched == "" {
		if err := i.store.DeadLetter(ctx, strings.Join(payload.Envelope.RcptTo, ","), payload.TaskID, "no matching account", raw); err != nil {
			return err
		}
		i.log.Warn("inbound message has no matching account", "task", payload.TaskID, "recipients", payload.Envelope.RcptTo)
		return nil
	}

	mailboxName := "INBOX"
	if payload.Verdicts != nil && (payload.Verdicts.IsSpam || payload.Verdicts.HasMalware) {
		mailboxName = "Junk"
	}
	mailbox, err := i.store.MailboxByName(ctx, account.ID, mailboxName)
	if errors.Is(err, store.ErrMailboxNotFound) {
		mailbox, err = i.store.CreateMailbox(ctx, account.ID, mailboxName, specialUse(mailboxName))
	}
	if err != nil {
		return fmt.Errorf("select mailbox %q: %w", mailboxName, err)
	}

	internal := time.Now().UTC()
	if payload.AcceptedAtUTC > 0 {
		internal = time.Unix(payload.AcceptedAtUTC, 0).UTC()
	}
	sum := sha256.Sum256(raw)
	_, inserted, err := i.store.InsertMessage(ctx, store.MessageInput{
		AccountID: account.ID, MailboxID: mailbox.ID,
		Raw: raw, RawSHA256: hex.EncodeToString(sum[:]), StoreData: true,
		Size: int64(len(raw)), InternalDate: internal,
		MessageID: firstNonEmpty(parsed.MessageID, payload.Message.MessageID),
		Subject:   firstNonEmpty(parsed.Subject, payload.Message.Subject),
		From:      convertAddresses(parsed.From), To: convertAddresses(parsed.To),
		Cc: convertAddresses(parsed.Cc), Bcc: convertAddresses(parsed.Bcc),
		ReplyTo:   convertAddresses(parsed.ReplyTo),
		InReplyTo: parsed.InReplyTo, References: parsed.References, SentAt: optionalTime(parsed.Date),
		Headers: convertHeaders(parsed.Headers), MIMETree: parsed.MIMETree,
		TaskID: payload.TaskID, IsSpam: mailboxName == "Junk",
		Search: store.SearchContent{
			Subject: parsed.Search.Subject, Sender: parsed.Search.Sender,
			Recipients: parsed.Search.Recipients, Headers: parsed.Search.Headers,
			Body: parsed.Search.Body, Attachments: parsed.Search.Attachments,
		},
		DedupeRaw: true,
	})
	if err != nil {
		return fmt.Errorf("store inbound message for task %s: %w", payload.TaskID, err)
	}
	if inserted {
		i.hub.Notify(mailbox.ID)
		i.log.Info("stored inbound message", "task", payload.TaskID, "account", account.PrimaryAddress,
			"mailbox", mailboxName, "bytes", len(raw))
	}
	if i.cfg.Storage.Mode == config.StorageS3 && i.cfg.Storage.S3.SpoolMaxBytes > 0 {
		if _, _, err := i.store.EnforceSpoolLimit(ctx, i.cfg.Storage.S3.SpoolMaxBytes); err != nil {
			i.log.Warn("enforce S3 spool limit", "error", err)
		}
	}
	return nil
}

func (i *Ingester) handleAttempt(ctx context.Context, attempt *notify.AttemptEvent) error {
	if attempt == nil || attempt.Channel != notify.ChannelS3 {
		return nil
	}
	if attempt.Status != webhook.StatusDelivered || attempt.ObjectKey == "" {
		if attempt.Status == webhook.StatusFailed || attempt.Status == webhook.StatusExpired {
			i.log.Warn("mxRaven S3 archive attempt failed",
				"task", attempt.TaskID, "correlation", attempt.CorrelationID,
				"reason", attempt.Reason, "message", attempt.Message)
		}
		return nil
	}
	message, err := i.store.SetS3Key(ctx, firstNonEmpty(attempt.CorrelationID, attempt.TaskID), attempt.ObjectKey)
	if err == nil {
		if !i.cfg.Storage.S3.Spool {
			if err := i.store.EvictLocalData(ctx, message.ID); err != nil {
				i.log.Warn("evict local spool copy", "message", message.ID, "error", err)
			}
		}
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// The accepted event may have expired. The object key carries the mailbox
	// address, so it is enough to ingest directly.
	if err := i.ingestS3Key(ctx, attempt.ObjectKey); err != nil {
		return err
	}
	return nil
}

func (i *Ingester) ingestS3Key(ctx context.Context, key string) error {
	if i.s3 == nil {
		return nil
	}
	if _, err := i.store.MessageByS3Key(ctx, key); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, err := i.store.BlobByRef(ctx, key); err == nil {
		// A refcount-zero blob is a deleted-message tombstone. Do not
		// resurrect mail the user expunged.
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	address, err := mailboxAddressFromKey(i.cfg.Storage.S3.KeyTemplate, key)
	if err != nil {
		i.log.Warn("cannot parse mailbox from S3 key", "key", key, "error", err)
		return nil
	}
	account, _, err := i.store.ResolveRecipient(ctx, address)
	if errors.Is(err, store.ErrNoRecipient) || errors.Is(err, store.ErrAccountDisabled) {
		i.log.Warn("S3 object has no matching account", "key", key, "address", address)
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := i.s3.Get(ctx, key)
	if err != nil {
		if errors.Is(err, blob.ErrObjectNotFound) {
			return fmt.Errorf("s3 object %s is missing", key)
		}
		return err
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	if existing, err := i.store.MessageByRawSHA(ctx, account.ID, sha); err == nil {
		if err := i.store.BindS3ByMessageID(ctx, existing.ID, key); err != nil {
			return err
		}
		if !i.cfg.Storage.S3.Spool {
			if err := i.store.EvictLocalData(ctx, existing.ID); err != nil {
				i.log.Warn("evict reconciled spool copy", "message", existing.ID, "error", err)
			}
		}
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return err
	}
	mailboxName := "INBOX"
	mailbox, err := i.store.MailboxByName(ctx, account.ID, mailboxName)
	if errors.Is(err, store.ErrMailboxNotFound) {
		mailbox, err = i.store.CreateMailbox(ctx, account.ID, mailboxName, specialUse(mailboxName))
	}
	if err != nil {
		return err
	}
	_, inserted, err := i.store.InsertMessage(ctx, store.MessageInput{
		AccountID: account.ID, MailboxID: mailbox.ID,
		Raw: raw, RawSHA256: sha, Kind: "s3", S3Key: key,
		Size: int64(len(raw)), InternalDate: time.Now().UTC(),
		MessageID: parsed.MessageID, Subject: parsed.Subject,
		From: convertAddresses(parsed.From), To: convertAddresses(parsed.To),
		Cc: convertAddresses(parsed.Cc), Bcc: convertAddresses(parsed.Bcc),
		ReplyTo:   convertAddresses(parsed.ReplyTo),
		InReplyTo: parsed.InReplyTo, References: parsed.References, SentAt: optionalTime(parsed.Date),
		Headers: convertHeaders(parsed.Headers), MIMETree: parsed.MIMETree,
		Search: store.SearchContent{
			Subject: parsed.Search.Subject, Sender: parsed.Search.Sender,
			Recipients: parsed.Search.Recipients, Headers: parsed.Search.Headers,
			Body: parsed.Search.Body, Attachments: parsed.Search.Attachments,
		},
		DedupeRaw: true,
	})
	if err != nil {
		return err
	}
	if inserted {
		i.hub.Notify(mailbox.ID)
		i.log.Info("reconciled S3 message", "key", key, "account", account.PrimaryAddress)
	}
	return nil
}

// ReconcileOnce runs one S3 bucket reconciliation pass.
func (i *Ingester) ReconcileOnce(ctx context.Context) error {
	return i.reconcileOnce(ctx)
}

func (i *Ingester) reconcileLoop(ctx context.Context) {
	interval := i.cfg.Storage.S3.ReconcileEvery
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if err := i.reconcileOnce(ctx); err != nil && ctx.Err() == nil {
		i.log.Warn("initial S3 reconciliation failed", "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := i.reconcileOnce(ctx); err != nil && ctx.Err() == nil {
				i.log.Warn("S3 reconciliation failed", "error", err)
			}
		}
	}
}

func (i *Ingester) reconcileOnce(ctx context.Context) error {
	if i.s3 == nil {
		return nil
	}
	objects, err := i.s3.List(ctx, "")
	if err != nil {
		return err
	}
	for _, object := range objects {
		if !strings.HasSuffix(strings.ToLower(object.Key), ".eml") {
			continue
		}
		if err := i.ingestS3Key(ctx, object.Key); err != nil {
			i.log.Warn("reconcile object failed", "key", object.Key, "error", err)
		}
	}
	return nil
}

func convertAddresses(in []mailparse.Address) []store.Address {
	out := make([]store.Address, 0, len(in))
	for _, address := range in {
		out = append(out, store.Address{Name: address.Name, Address: address.Address})
	}
	return out
}

func convertHeaders(in []mailparse.Header) []store.Header {
	out := make([]store.Header, 0, len(in))
	for _, header := range in {
		out = append(out, store.Header{Name: header.Name, Value: header.Value})
	}
	return out
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func specialUse(name string) string {
	switch strings.ToLower(name) {
	case "junk":
		return `\Junk`
	case "sent":
		return `\Sent`
	case "drafts":
		return `\Drafts`
	case "trash":
		return `\Trash`
	case "archive":
		return `\Archive`
	default:
		return ""
	}
}
