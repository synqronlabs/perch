// Command demo-provision configures the mxRaven submission listener used by the
// perch live demo. It sets a rejecting default terminal action and adds one
// allow rule for the demo domain.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-faster/jx"
	"github.com/google/uuid"
	"github.com/synqronlabs/mxraven-go/admin"
	"github.com/synqronlabs/mxraven-go/admin/api"
	"github.com/synqronlabs/perch/internal/mxauth"
)

func main() {
	workspace := flag.String("workspace", "kush", "mxRaven workspace slug")
	listenerID := flag.String("listener", "", "submission listener ID")
	domain := flag.String("domain", "tryperch.mxraven.com", "demo domain")
	flag.Parse()
	if *listenerID == "" {
		fatal("--listener is required")
	}
	id, err := uuid.Parse(*listenerID)
	if err != nil {
		fatal("parse listener: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	authCfg := mxauth.Config{
		Issuer:       mxauth.DefaultIssuer,
		DashboardURL: mxauth.DefaultDashboardURL,
		ClientID:     mxauth.DefaultClientID,
	}
	auth, err := mxauth.Start(ctx, authCfg)
	if err != nil {
		fatal("start device authorization: %v", err)
	}
	fmt.Println("Approve perch in your browser:")
	fmt.Printf("  %s\n\n", auth.ConsentURL(authCfg.DashboardURL))
	fmt.Printf("Code: %s\n\n", auth.UserCode)
	fmt.Println("Waiting for approval...")
	token, err := mxauth.Wait(ctx, authCfg, auth)
	if err != nil {
		fatal("authorize: %v", err)
	}

	client, err := admin.New(admin.WithToken(token.AccessToken))
	if err != nil {
		fatal("create admin client: %v", err)
	}
	params := api.UpdateListenerDefaultTerminalActionParams{
		Slug:       api.TenantSlug(*workspace),
		ListenerID: id,
	}
	rejectPayload := api.DefaultTerminalActionPayload{
		"smtp_status_code":     jx.Raw("550"),
		"enhanced_status_code": jx.Raw(`"5.7.1"`),
		"message":              jx.Raw(`"The perch live demo only accepts @tryperch.mxraven.com recipients"`),
		"audit_reason":         jx.Raw(`"perch_demo_external_recipient_blocked"`),
	}
	res, err := client.UpdateListenerDefaultTerminalAction(ctx, &api.UpdateListenerDefaultTerminalActionRequest{
		ActionType:    api.DefaultTerminalActionTypeREJECT,
		ActionPayload: api.OptDefaultTerminalActionPayload{Set: true, Value: rejectPayload},
	}, params)
	if err != nil {
		fatal("set default terminal action: %v", err)
	}
	if problem, ok := admin.ProblemFrom(res); ok {
		fatal("set default terminal action: %s: %s", problem.Code, problem.Detail)
	}
	fmt.Println("submission listener default terminal action: REJECT")

	expr := fmt.Sprintf(`len(envelope.rcpt_to) <= 5 && all(envelope.rcpt_to, # endsWith "@%s")`, *domain)
	listParams := api.ListRoutingRulesParams{Slug: api.TenantSlug(*workspace), ListenerID: id}
	listRes, err := client.ListRoutingRules(ctx, listParams)
	if err != nil {
		fatal("list routing rules: %v", err)
	}
	if problem, ok := admin.ProblemFrom(listRes); ok {
		fatal("list routing rules: %s: %s", problem.Code, problem.Detail)
	}
	existing := false
	if okRes, ok := listRes.(*api.ListRoutingRulesOKHeaders); ok {
		for _, rule := range okRes.Response {
			fmt.Printf("existing rule priority=%d active=%t action=%s expr=%s\n", rule.Priority, rule.IsActive, rule.Action.Type, rule.ExpressionText)
			if rule.ExpressionText == expr && rule.Action.Type == api.DeliverActionRoutingRuleAction {
				existing = true
			}
		}
	} else {
		fatal("list routing rules: unexpected response %T", listRes)
	}
	if existing {
		fmt.Println("allow rule already exists")
		return
	}
	action := api.RoutingRuleAction{
		Type: api.DeliverActionRoutingRuleAction,
		DeliverAction: api.DeliverAction{
			ActionType:    api.DeliverActionActionTypeDELIVER,
			ActionPayload: api.EmptyActionPayload{},
		},
	}
	createRes, err := client.CreateRoutingRule(ctx, &api.CreateRoutingRuleRequest{
		Priority:       10,
		ExpressionText: expr,
		Action:         action,
	}, api.CreateRoutingRuleParams{Slug: api.TenantSlug(*workspace), ListenerID: id})
	if err != nil {
		fatal("create allow rule: %v", err)
	}
	if problem, ok := admin.ProblemFrom(createRes); ok {
		fatal("create allow rule: %s: %s", problem.Code, problem.Detail)
	}
	fmt.Printf("created allow rule: %s\n", expr)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "demo-provision: "+format+"\n", args...)
	os.Exit(1)
}
