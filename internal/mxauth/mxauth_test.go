package mxauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func discoveryServer(t *testing.T, handleToken http.HandlerFunc) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer":                        server.URL,
				"device_authorization_endpoint": server.URL + "/oauth/v2/device_authorization",
				"token_endpoint":                server.URL + "/oauth/v2/token",
			})
		case "/oauth/v2/device_authorization":
			writeJSON(t, w, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-1234",
				"verification_uri":          server.URL + "/device",
				"verification_uri_complete": server.URL + "/device?user_code=ABCD-1234",
				"expires_in":                600,
				"interval":                  1,
			})
		case "/oauth/v2/token":
			handleToken(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func testConfig(server *httptest.Server) Config {
	return Config{
		Issuer:       server.URL,
		DashboardURL: server.URL,
		ClientID:     "perch-client",
		HTTPClient:   server.Client(),
	}
}

func TestStartAndWait(t *testing.T) {
	polls := 0
	server := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(t, w, map[string]any{"error": "authorization_pending"})
			return
		}
		writeJSON(t, w, map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	cfg := testConfig(server)
	auth, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserCode != "ABCD-1234" {
		t.Fatalf("user code = %q", auth.UserCode)
	}
	if got, want := auth.ConsentURL(cfg.DashboardURL), server.URL+"/connect?user_code=ABCD-1234"; got != want {
		t.Fatalf("consent URL = %q, want %q", got, want)
	}
	token, err := Wait(context.Background(), cfg, auth)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access-1" || token.RefreshToken != "refresh-1" {
		t.Fatalf("token = %#v", token)
	}
	if token.ExpiresAt.Before(time.Now()) {
		t.Fatal("token already expired")
	}
}

func TestWaitRejectsDenied(t *testing.T) {
	server := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(t, w, map[string]any{"error": "access_denied"})
	})
	_, err := Wait(context.Background(), testConfig(server), Authorization{
		DeviceCode: "device-1",
		ExpiresIn:  60,
		Interval:   1,
	})
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("error = %v, want ErrAccessDenied", err)
	}
}

func TestTokenSourceRefreshes(t *testing.T) {
	calls := 0
	server := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q", got)
		}
		if got := r.Form.Get("refresh_token"); got != "refresh-old" {
			t.Fatalf("refresh_token = %q", got)
		}
		writeJSON(t, w, map[string]any{
			"access_token":  "access-new",
			"refresh_token": "refresh-new",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	source := NewTokenSource(testConfig(server), &Token{
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "access-new" || calls != 1 {
		t.Fatalf("token = %q, calls = %d", token, calls)
	}
	token, err = source.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "access-new" || calls != 1 {
		t.Fatalf("second token = %q, calls = %d", token, calls)
	}
}

func TestValidateRequiresClientID(t *testing.T) {
	err := (Config{}).Validate()
	if err == nil {
		t.Fatal("expected an error without a client ID")
	}
}
