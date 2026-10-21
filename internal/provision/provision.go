// Package provision creates the mxRaven domain, listeners, keys, routes, and
// S3 archive rule needed by a perch installation.
package provision

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/synqronlabs/mxraven-go/admin"
	"github.com/synqronlabs/mxraven-go/admin/api"
	"github.com/synqronlabs/perch/internal/config"
)

// Record is one DNS instruction returned by mxRaven.
type Record struct {
	Name     string
	Type     string
	Value    string
	Priority int32
	Purpose  string
}

// Options controls provisioning.
type Options struct {
	BaseURL          string
	Token            string
	Tenant           string
	Hostname         string
	DataDir          string
	Domain           string
	InboundDomain    string
	SendingDomain    string
	EnableSubmission bool
	Storage          config.StorageConfig
	DryRun           bool
}

// Result is the generated configuration and the DNS records the user must
// publish.
type Result struct {
	Config config.Config
	DNS    []Record
	Notes  []string
}

// Run creates or reuses the mxRaven resources. It never stores the
// control-plane token.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("provision: control-plane token is required")
	}
	if strings.TrimSpace(opts.Tenant) == "" {
		return nil, errors.New("provision: workspace slug is required")
	}
	if strings.TrimSpace(opts.Domain) == "" {
		return nil, errors.New("provision: mail domain is required")
	}
	inboundDomain := firstNonEmpty(opts.InboundDomain, opts.Domain)
	sendingDomain := firstNonEmpty(opts.SendingDomain, opts.Domain)
	cfg := config.Default()
	if opts.Storage.Mode == "" {
		opts.Storage.Mode = config.StorageSQLite
	}
	if opts.Storage.Mode == config.StorageS3 && opts.Storage.S3.KeyTemplate == "" {
		opts.Storage.S3.KeyTemplate = "perch/{{.recipient.local}}/{{.recipient.domain}}/{{.task.id}}.eml"
	}
	cfg.DataDir = opts.DataDir
	cfg.Hostname = opts.Hostname
	cfg.Storage = opts.Storage
	cfg.Setup.Domain = opts.Domain
	cfg.Setup.InboundDomain = inboundDomain
	cfg.Setup.SendingDomain = sendingDomain
	result := &Result{Config: cfg}

	if opts.DryRun {
		result.Notes = []string{
			"would create or reuse sending domain " + sendingDomain,
			"would create MTA listener and inbound route for " + inboundDomain,
			"would enable client notifications and mint an MTA API key",
		}
		if opts.EnableSubmission {
			result.Notes = append(result.Notes, "would create submission listener, sending-domain policy, and API key")
		}
		if opts.Storage.Mode == config.StorageS3 {
			result.Notes = append(result.Notes, "would create S3 integration and S3_STORE routing rule")
		}
		return result, nil
	}

	adminOpts := []admin.Option{admin.WithToken(opts.Token)}
	if strings.TrimSpace(opts.BaseURL) != "" {
		adminOpts = append(adminOpts, admin.WithBaseURL(opts.BaseURL))
	}
	client, err := admin.New(adminOpts...)
	if err != nil {
		return nil, fmt.Errorf("create mxRaven admin client: %w", err)
	}
	ws := client.Workspace(opts.Tenant)
	if _, err := ws.Tenant.Get(ctx); err != nil {
		return nil, fmt.Errorf("validate workspace %q: %w", opts.Tenant, err)
	}

	sending, err := ensureDomain(ctx, ws, sendingDomain)
	if err != nil {
		return nil, err
	}
	result.DNS = append(result.DNS, records(sending.RequiredCustomerRecords)...)

	mta, err := ensureMTA(ctx, ws)
	if err != nil {
		return nil, err
	}
	result.Config.Setup.MTAListenerID = mta.ID.String()
	notification, err := ws.Listeners.SetNotifications(ctx, mta.ID, true)
	if err != nil {
		return nil, fmt.Errorf("enable MTA notifications: %w", err)
	}
	mtaKey, err := ws.APIKeys.Create(ctx, mta.ID)
	if err != nil {
		return nil, fmt.Errorf("create MTA API key: %w", err)
	}
	result.Config.MXRaVen.Listeners = append(result.Config.MXRaVen.Listeners, config.ListenerConfig{
		ID: mta.ID.String(), Kind: "mta", KeyPrefix: mtaKey.Username,
		KeySecret: mtaKey.Secret, Consumer: notification.Consumer,
	})

	route, err := ensureInboundRoute(ctx, ws, mta.ID, inboundDomain)
	if err != nil {
		return nil, err
	}
	result.DNS = append(result.DNS, records(route.RequiredCustomerRecords)...)

	if opts.Storage.Mode == config.StorageS3 {
		ref := storageRef(opts.Storage.S3, inboundDomain)
		if err := ensureStorage(ctx, ws, ref, opts.Storage.S3); err != nil {
			return nil, err
		}
		if err := ensureS3Rule(ctx, ws, mta.ID, ref, opts.Storage.S3); err != nil {
			return nil, err
		}
		result.Config.Setup.StorageRef = ref
	}

	if opts.EnableSubmission {
		submission, err := ensureSubmission(ctx, ws)
		if err != nil {
			return nil, err
		}
		result.Config.Setup.SubListenerID = submission.ID.String()
		if _, err := ws.SendingDomainPolicies.Replace(ctx, submission.ID, api.ReplaceSendingDomainPolicyRequest{
			Grants: []api.SendingDomainPolicyGrantRequest{{
				DomainID: sending.ID, SubdomainScope: api.SendingDomainSubdomainScopeExact,
			}},
		}); err != nil {
			return nil, fmt.Errorf("authorize sending domain on submission listener: %w", err)
		}
		subKey, err := ws.APIKeys.Create(ctx, submission.ID)
		if err != nil {
			return nil, fmt.Errorf("create submission API key: %w", err)
		}
		result.Config.MXRaVen.Listeners = append(result.Config.MXRaVen.Listeners, config.ListenerConfig{
			ID: submission.ID.String(), Kind: "submission",
			KeyPrefix: subKey.Username, KeySecret: subKey.Secret,
		})
	}

	result.Config.MXRaVen.SubmissionHost = "smtp.mxraven.email"
	result.Config.MXRaVen.SubmissionPort = 587
	result.Config.MXRaVen.NotificationURL = "wss://notifications.mxraven.com"
	result.DNS = dedupeRecords(result.DNS)
	return result, nil
}

func ensureDomain(ctx context.Context, ws *admin.Workspace, name string) (*api.Domain, error) {
	for domain, err := range ws.Domains.List(ctx, admin.ListDomainsOptions{}) {
		if err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		if strings.EqualFold(domain.DomainName, name) {
			return &domain, nil
		}
	}
	domain, err := ws.Domains.Create(ctx, admin.CreateDomainInput{DomainName: name})
	if err != nil {
		return nil, fmt.Errorf("create sending domain %q: %w", name, err)
	}
	return domain, nil
}

func ensureMTA(ctx context.Context, ws *admin.Workspace) (*api.Listener, error) {
	for listener, err := range ws.Listeners.List(ctx, admin.ListListenersOptions{ListenerType: api.ListenerTypeMta}) {
		if err != nil {
			return nil, fmt.Errorf("list MTA listeners: %w", err)
		}
		if strings.EqualFold(listener.DisplayName, "perch inbound") {
			return &listener, nil
		}
	}
	listener, err := ws.Listeners.Create(ctx, admin.CreateListenerInput{
		DisplayName: "perch inbound", ListenerType: api.ListenerTypeMta,
		StreamType:                   api.CreatableListenerStreamTypeTransactional,
		DefaultTerminalActionType:    api.DefaultTerminalActionTypeDROP,
		DefaultTerminalActionPayload: admin.DropPayload("perch mailbox ingest"),
	})
	if err != nil {
		return nil, fmt.Errorf("create MTA listener: %w", err)
	}
	return listener, nil
}

func ensureSubmission(ctx context.Context, ws *admin.Workspace) (*api.Listener, error) {
	for listener, err := range ws.Listeners.List(ctx, admin.ListListenersOptions{ListenerType: api.ListenerTypeSubmission}) {
		if err != nil {
			return nil, fmt.Errorf("list submission listeners: %w", err)
		}
		if strings.EqualFold(listener.DisplayName, "perch submission") {
			return &listener, nil
		}
	}
	listener, err := ws.Listeners.Create(ctx, admin.CreateListenerInput{
		DisplayName: "perch submission", ListenerType: api.ListenerTypeSubmission,
		StreamType:                   api.CreatableListenerStreamTypeTransactional,
		DefaultTerminalActionType:    api.DefaultTerminalActionTypeDELIVER,
		DefaultTerminalActionPayload: admin.DeliverPayload(),
	})
	if err != nil {
		return nil, fmt.Errorf("create submission listener: %w", err)
	}
	return listener, nil
}

func ensureInboundRoute(ctx context.Context, ws *admin.Workspace, listenerID uuid.UUID, domain string) (*api.InboundRoute, error) {
	for route, err := range ws.InboundRoutes.List(ctx, admin.ListInboundRoutesOptions{}) {
		if err != nil {
			return nil, fmt.Errorf("list inbound routes: %w", err)
		}
		if strings.EqualFold(route.DomainName, domain) {
			return &route, nil
		}
	}
	route, err := ws.InboundRoutes.Create(ctx, api.CreateInboundRouteRequest{
		MtaListenerID: listenerID, DomainName: domain,
		OnboardDomainIfMissing: api.NewOptBool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("create inbound route %q: %w", domain, err)
	}
	return route, nil
}

func ensureStorage(ctx context.Context, ws *admin.Workspace, ref string, cfg config.S3Config) error {
	if _, err := ws.StorageIntegrations.GetByRef(ctx, ref); err == nil {
		return nil
	}
	_, err := ws.StorageIntegrations.Create(ctx, api.CreateStorageIntegrationRequest{
		StorageRef: ref, DisplayName: "perch archive",
		AccessKey: cfg.AccessKey, SecretKey: cfg.Secret(), BucketName: cfg.Bucket,
		Region: cfg.Region, EndpointURL: cfg.Endpoint, ForcePathStyle: cfg.ForcePathStyle,
	})
	if err != nil {
		return fmt.Errorf("create S3 storage integration: %w", err)
	}
	return nil
}

func ensureS3Rule(ctx context.Context, ws *admin.Workspace, listenerID uuid.UUID, ref string, cfg config.S3Config) error {
	for rule, err := range ws.RoutingRules.List(ctx, listenerID, admin.ListRoutingRulesOptions{ActionType: api.RoutingRuleActionKindS3STORE}) {
		if err != nil {
			return fmt.Errorf("list routing rules: %w", err)
		}
		if rule.Action.Type == api.S3StoreActionRoutingRuleAction {
			return nil
		}
	}
	_, err := ws.RoutingRules.Create(ctx, listenerID, api.CreateRoutingRuleRequest{
		Priority: 10, ExpressionText: "true",
		Action: api.RoutingRuleAction{
			Type: api.S3StoreActionRoutingRuleAction,
			S3StoreAction: api.S3StoreAction{
				ActionType: api.S3StoreActionActionTypeS3STORE,
				ActionPayload: api.S3StoreActionPayload{
					StorageRef: ref, ObjectKeyTemplate: api.NewOptString(cfg.KeyTemplate),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create S3_STORE routing rule: %w", err)
	}
	return nil
}

func records(in []api.DNSInstructionRecord) []Record {
	out := make([]Record, 0, len(in))
	for _, record := range in {
		item := Record{Name: record.Name, Type: string(record.Type), Value: record.Value}
		if record.Priority.Set {
			item.Priority = record.Priority.Value
		}
		if record.Purpose.Set {
			item.Purpose = record.Purpose.Value
		}
		out = append(out, item)
	}
	return out
}

func dedupeRecords(in []Record) []Record {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, record := range in {
		key := record.Type + "|" + record.Name + "|" + record.Value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	return out
}

func storageRef(_ config.S3Config, domain string) string {
	return "perch-" + strings.ReplaceAll(domain, ".", "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
