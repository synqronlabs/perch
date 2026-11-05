// Package mxauth implements the mxRaven connected-application device
// authorization flow (RFC 8628) used by perch setup.
//
// Perch is a native client: it asks mxRaven for a code, the workspace owner
// approves the application in the dashboard, and the identity provider issues
// a short-lived access token plus a refresh token. The tokens exist in memory
// for the duration of setup only; perch never writes a control-plane credential
// to its configuration file.
package mxauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultIssuer is the production mxRaven identity provider.
	DefaultIssuer = "https://id.mxraven.com"
	// DefaultDashboardURL is the production consent surface.
	DefaultDashboardURL = "https://app.mxraven.com"

	// DefaultClientID is the public client identifier of the production
	// "perch" connected application. It is not a secret; callers can override
	// it with --client-id or MXRAVEN_CLIENT_ID for staging or local builds.
	DefaultClientID = "398301308521611673"

	// DeviceGrantType is the RFC 8628 device-code grant.
	DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// DefaultScopes ask for the user's project roles and a refresh token. The
	// control-plane permissions shown on the consent screen come from the
	// connected application's registration, not from these OAuth scopes.
	DefaultScopes = "openid profile email offline_access urn:zitadel:iam:org:projects:roles"
)

const (
	defaultPollInterval = 5 * time.Second
	defaultLifetime     = 10 * time.Minute
	requestTimeout      = 30 * time.Second
)

// ErrAccessDenied means the owner declined the application.
var ErrAccessDenied = errors.New("mxauth: access denied")

// ErrExpired means the device code expired before it was approved.
var ErrExpired = errors.New("mxauth: device code expired")

// Config describes one mxRaven identity deployment.
type Config struct {
	Issuer       string
	DashboardURL string
	ClientID     string
	Scopes       string
	HTTPClient   *http.Client
}

// Normalize fills defaults and trims trailing slashes.
func (c Config) Normalize() Config {
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	if c.Issuer == "" {
		c.Issuer = DefaultIssuer
	}
	c.DashboardURL = strings.TrimRight(strings.TrimSpace(c.DashboardURL), "/")
	if c.DashboardURL == "" {
		c.DashboardURL = DefaultDashboardURL
	}
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.Scopes = strings.TrimSpace(c.Scopes)
	if c.Scopes == "" {
		c.Scopes = DefaultScopes
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: requestTimeout}
	}
	return c
}

// Validate reports whether the configuration can start a device flow.
func (c Config) Validate() error {
	c = c.Normalize()
	if c.ClientID == "" {
		return errors.New("mxauth: no client ID configured; register the perch connected application and set --client-id or MXRAVEN_CLIENT_ID")
	}
	if _, err := url.ParseRequestURI(c.Issuer); err != nil {
		return fmt.Errorf("mxauth: invalid issuer %q: %w", c.Issuer, err)
	}
	if _, err := url.ParseRequestURI(c.DashboardURL); err != nil {
		return fmt.Errorf("mxauth: invalid dashboard URL %q: %w", c.DashboardURL, err)
	}
	return nil
}

// Authorization is the pending device request returned by the issuer.
type Authorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

// ConsentURL is the mxRaven dashboard address the user opens. The dashboard
// records the workspace consent before handing the browser to the identity
// provider's device confirmation page.
func (a Authorization) ConsentURL(dashboardURL string) string {
	base := strings.TrimRight(strings.TrimSpace(dashboardURL), "/")
	if base == "" {
		base = DefaultDashboardURL
	}
	return base + "/connect?user_code=" + url.QueryEscape(a.UserCode)
}

// Token is an OAuth token response.
type Token struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	ExpiresAt    time.Time
}

func (t *Token) valid(now time.Time) bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	if t.ExpiresAt.IsZero() {
		return true
	}
	return now.Add(30 * time.Second).Before(t.ExpiresAt)
}

// Start discovers the issuer and begins a device authorization request.
func Start(ctx context.Context, cfg Config) (Authorization, error) {
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return Authorization{}, err
	}
	metadata, err := discover(ctx, cfg)
	if err != nil {
		return Authorization{}, err
	}
	form := url.Values{
		"client_id": {cfg.ClientID},
		"scope":     {cfg.Scopes},
	}
	var response struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := postForm(ctx, cfg, metadata.DeviceAuthorizationEndpoint, form, &response); err != nil {
		return Authorization{}, fmt.Errorf("mxauth: start device authorization: %w", err)
	}
	if response.DeviceCode == "" || response.UserCode == "" {
		return Authorization{}, errors.New("mxauth: device authorization response is missing a code")
	}
	if response.ExpiresIn <= 0 {
		response.ExpiresIn = int(defaultLifetime / time.Second)
	}
	if response.Interval <= 0 {
		response.Interval = int(defaultPollInterval / time.Second)
	}
	return Authorization{
		DeviceCode:              response.DeviceCode,
		UserCode:                response.UserCode,
		VerificationURI:         response.VerificationURI,
		VerificationURIComplete: response.VerificationURIComplete,
		ExpiresIn:               response.ExpiresIn,
		Interval:                response.Interval,
	}, nil
}

// Wait polls the token endpoint until the owner approves the request or the
// code expires. The caller is responsible for showing the code and URL.
func Wait(ctx context.Context, cfg Config, auth Authorization) (*Token, error) {
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	metadata, err := discover(ctx, cfg)
	if err != nil {
		return nil, err
	}
	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = defaultPollInterval
	}
	lifetime := time.Duration(auth.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = defaultLifetime
	}
	deadline := time.Now().Add(lifetime)
	for {
		token, retry, slowDown, err := poll(ctx, cfg, metadata.TokenEndpoint, auth.DeviceCode)
		if token != nil {
			return token, nil
		}
		if !retry {
			return nil, err
		}
		if slowDown {
			interval += 5 * time.Second
		}
		if time.Now().After(deadline) {
			return nil, ErrExpired
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func poll(ctx context.Context, cfg Config, tokenEndpoint, deviceCode string) (*Token, bool, bool, error) {
	form := url.Values{
		"grant_type":  {DeviceGrantType},
		"device_code": {deviceCode},
		"client_id":   {cfg.ClientID},
	}
	var response struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := postForm(ctx, cfg, tokenEndpoint, form, &response); err != nil {
		var oauthErr *OAuthError
		if errors.As(err, &oauthErr) {
			switch oauthErr.Code {
			case "authorization_pending":
				return nil, true, false, nil
			case "slow_down":
				return nil, true, true, nil
			case "access_denied":
				return nil, false, false, ErrAccessDenied
			case "expired_token":
				return nil, false, false, ErrExpired
			}
		}
		return nil, false, false, fmt.Errorf("mxauth: poll device authorization: %w", err)
	}
	if response.Error != "" {
		switch response.Error {
		case "authorization_pending":
			return nil, true, false, nil
		case "slow_down":
			return nil, true, true, nil
		case "access_denied":
			return nil, false, false, ErrAccessDenied
		case "expired_token":
			return nil, false, false, ErrExpired
		}
		return nil, false, false, &OAuthError{Code: response.Error, Description: response.ErrorDescription}
	}
	if response.AccessToken == "" {
		return nil, false, false, errors.New("mxauth: token response is missing an access token")
	}
	return &Token{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		TokenType:    response.TokenType,
		ExpiresIn:    response.ExpiresIn,
		ExpiresAt:    expiry(response.ExpiresIn),
	}, false, false, nil
}

// TokenSource returns an admin.WithTokenSource-compatible callback that
// refreshes the access token when it nears expiry.
type TokenSource struct {
	cfg   Config
	mu    sync.Mutex
	token Token
}

// NewTokenSource creates a refreshing token source.
func NewTokenSource(cfg Config, token *Token) *TokenSource {
	cfg = cfg.Normalize()
	source := &TokenSource{cfg: cfg}
	if token != nil {
		source.token = *token
	}
	return source
}

// Token returns a valid access token, refreshing it when necessary.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token.valid(time.Now()) {
		return s.token.AccessToken, nil
	}
	if s.token.RefreshToken == "" {
		return "", errors.New("mxauth: access token expired and no refresh token is available; run perch setup again")
	}
	metadata, err := discover(ctx, s.cfg)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.token.RefreshToken},
		"client_id":     {s.cfg.ClientID},
	}
	var response struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := postForm(ctx, s.cfg, metadata.TokenEndpoint, form, &response); err != nil {
		return "", fmt.Errorf("mxauth: refresh access token: %w", err)
	}
	if response.AccessToken == "" {
		return "", errors.New("mxauth: refresh response is missing an access token")
	}
	s.token.AccessToken = response.AccessToken
	if response.RefreshToken != "" {
		s.token.RefreshToken = response.RefreshToken
	}
	s.token.TokenType = response.TokenType
	s.token.ExpiresIn = response.ExpiresIn
	s.token.ExpiresAt = expiry(response.ExpiresIn)
	return s.token.AccessToken, nil
}

func expiry(seconds int) time.Time {
	if seconds <= 0 {
		seconds = 3600
	}
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

type metadata struct {
	Issuer                      string `json:"issuer"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

func discover(ctx context.Context, cfg Config) (metadata, error) {
	endpoint := cfg.Issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return metadata{}, fmt.Errorf("mxauth: build discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	response, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return metadata{}, fmt.Errorf("mxauth: discover issuer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return metadata{}, fmt.Errorf("mxauth: discover issuer: status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result metadata
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return metadata{}, fmt.Errorf("mxauth: decode discovery document: %w", err)
	}
	if result.Issuer == "" || result.DeviceAuthorizationEndpoint == "" || result.TokenEndpoint == "" {
		return metadata{}, errors.New("mxauth: discovery document is missing required endpoints")
	}
	if strings.TrimRight(result.Issuer, "/") != cfg.Issuer {
		return metadata{}, fmt.Errorf("mxauth: discovery issuer %q does not match %q", result.Issuer, cfg.Issuer)
	}
	for _, endpoint := range []string{result.DeviceAuthorizationEndpoint, result.TokenEndpoint} {
		if err := validateEndpoint(endpoint); err != nil {
			return metadata{}, err
		}
	}
	return result, nil
}

func validateEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("mxauth: invalid issuer endpoint: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("mxauth: issuer endpoint %q must use http or https", value)
	}
	return nil
}

// OAuthError is an error response from the token endpoint.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

func postForm(ctx context.Context, cfg Config, endpoint string, form url.Values, out any) error {
	body := bytes.NewBufferString(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var oauthErr OAuthError
		if err := json.Unmarshal(data, &oauthErr); err == nil && oauthErr.Code != "" {
			return &oauthErr
		}
		return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// OpenBrowser opens rawurl with the platform's default browser. It returns an
// error when no opener could be started; callers treat that as non-fatal
// because the URL is also printed.
func OpenBrowser(rawurl string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawurl)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawurl)
	default:
		cmd = exec.Command("xdg-open", rawurl)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
