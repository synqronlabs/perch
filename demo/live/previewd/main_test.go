package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestServer(verifyURL string) *server {
	return &server{
		cfg: config{
			TurnstileSiteKey: "site-key",
			TurnstileSecret:  "secret-key",
			turnstileURL:     verifyURL,
		},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		sessions:   make(map[string]*session),
		byIP:       make(map[string]map[string]struct{}),
		creates:    make(map[string][]time.Time),
		verifies:   make(map[string][]time.Time),
	}
}

func TestVerifyTurnstileSuccess(t *testing.T) {
	var got url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got = r.PostForm
		_, _ = io.WriteString(w, `{"success":true,"action":"demo_session"}`)
	}))
	defer ts.Close()

	s := newTestServer(ts.URL)
	if err := s.verifyTurnstile(context.Background(), "token-value", "203.0.113.7"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Get("secret") != "secret-key" {
		t.Errorf("secret = %q", got.Get("secret"))
	}
	if got.Get("response") != "token-value" {
		t.Errorf("response = %q", got.Get("response"))
	}
	if got.Get("remoteip") != "203.0.113.7" {
		t.Errorf("remoteip = %q", got.Get("remoteip"))
	}
}

func TestVerifyTurnstileRejects(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "unsuccessful", status: http.StatusOK, body: `{"success":false,"error-codes":["invalid-input-response"]}`, want: errTurnstileRejected},
		{name: "wrong action", status: http.StatusOK, body: `{"success":true,"action":"other"}`, want: errTurnstileRejected},
		{name: "upstream error", status: http.StatusInternalServerError, body: `{}`, want: errTurnstileUnavailable},
		{name: "bad json", status: http.StatusOK, body: `not json`, want: errTurnstileUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer ts.Close()

			s := newTestServer(ts.URL)
			err := s.verifyTurnstile(context.Background(), "token-value", "203.0.113.7")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyTurnstileEmptyToken(t *testing.T) {
	s := newTestServer("http://127.0.0.1:1")
	err := s.verifyTurnstile(context.Background(), "", "203.0.113.7")
	if !errors.Is(err, errTurnstileRejected) {
		t.Fatalf("error = %v, want %v", err, errTurnstileRejected)
	}
}

func TestVerifyTurnstileDisabled(t *testing.T) {
	s := newTestServer("http://127.0.0.1:1")
	s.cfg.TurnstileSiteKey = ""
	s.cfg.TurnstileSecret = ""
	if err := s.verifyTurnstile(context.Background(), "", ""); err != nil {
		t.Fatalf("verify with turnstile disabled: %v", err)
	}
}

func TestAllowVerifyRateLimit(t *testing.T) {
	s := newTestServer("")
	now := time.Now().UTC()
	for i := 0; i < maxVerifiesPerHour; i++ {
		if !s.allowVerify("203.0.113.7", now) {
			t.Fatalf("attempt %d unexpectedly limited", i+1)
		}
	}
	if s.allowVerify("203.0.113.7", now) {
		t.Fatal("attempt past the limit was allowed")
	}
	if !s.allowVerify("203.0.113.8", now) {
		t.Fatal("limit applied to the wrong address")
	}
}

func TestSessionRequiresTurnstileToken(t *testing.T) {
	s := newTestServer("http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:1234"
	rec := httptest.NewRecorder()

	s.handleSession(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSessionPostReusesExistingSession(t *testing.T) {
	s := newTestServer("http://127.0.0.1:1")
	sess := &session{ID: "abc123", Address: "demo@example.com", Expires: time.Now().Add(time.Minute)}
	s.sessions[sess.ID] = sess
	req := httptest.NewRequest(http.MethodPost, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: "perch_demo", Value: sess.ID})
	rec := httptest.NewRecorder()

	s.handleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSessionReportsVerificationUnavailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	s := newTestServer(ts.URL)
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"turnstile_token":"token-value"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:1234"
	rec := httptest.NewRecorder()

	s.handleSession(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestConfigReportsTurnstileSiteKey(t *testing.T) {
	s := newTestServer("")
	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"turnstile_site_key":"site-key"`) {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(body, `"turnstile_required":true`) {
		t.Fatalf("body = %s", body)
	}
}
