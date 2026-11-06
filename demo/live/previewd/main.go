package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

//go:embed web/*
var webFiles embed.FS

type config struct {
	Listen       string
	PerchConfig  string
	PerchBin     string
	Domain       string
	MailHost     string
	IMAPPort     int
	SMTPPort     int
	TTL          time.Duration
	MaxSessions  int
	Image        string
	Network      string
	ProxyIP      string
	StateDir     string
	DockerBin    string
	SecureCookie bool

	TurnstileSiteKey string
	TurnstileSecret  string
	turnstileURL     string
}

type session struct {
	ID          string
	Address     string
	Password    string
	Created     time.Time
	Expires     time.Time
	ContainerID string
	Dir         string
	IP          string
}

type sessionView struct {
	Address    string    `json:"address"`
	Password   string    `json:"password"`
	ExpiresAt  time.Time `json:"expires_at"`
	TTLSeconds int       `json:"ttl_seconds"`
	IMAPHost   string    `json:"imap_host"`
	IMAPPort   int       `json:"imap_port"`
	SMTPHost   string    `json:"smtp_host"`
	SMTPPort   int       `json:"smtp_port"`
}

type server struct {
	cfg        config
	log        *slog.Logger
	upgrader   websocket.Upgrader
	mu         sync.Mutex
	sessions   map[string]*session
	byIP       map[string]map[string]struct{}
	creates    map[string][]time.Time
	verifies   map[string][]time.Time
	creating   int
	createSem  chan struct{}
	httpClient *http.Client
}

func main() {
	var cfg config
	flag.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&cfg.PerchConfig, "perch-config", "/etc/perch-demo/perch.yaml", "shared perch config")
	flag.StringVar(&cfg.PerchBin, "perch-bin", "/usr/local/bin/perch", "perch binary")
	flag.StringVar(&cfg.Domain, "domain", "tryperch.mxraven.com", "demo account domain")
	flag.StringVar(&cfg.MailHost, "mail-host", "mail.tryperch.mxraven.com", "public IMAP/SMTP host")
	flag.IntVar(&cfg.IMAPPort, "imap-port", 993, "public IMAPS port")
	flag.IntVar(&cfg.SMTPPort, "smtp-port", 465, "public SMTPS port")
	flag.DurationVar(&cfg.TTL, "ttl", 15*time.Minute, "demo session lifetime")
	flag.IntVar(&cfg.MaxSessions, "max-sessions", 50, "maximum concurrent demo sessions")
	flag.StringVar(&cfg.Image, "terminal-image", "perch-demo-terminal:latest", "terminal container image")
	flag.StringVar(&cfg.Network, "docker-network", "perch-demo-internal", "internal Docker network")
	flag.StringVar(&cfg.ProxyIP, "proxy-ip", "172.31.240.2", "Docker proxy address for the mail host")
	flag.StringVar(&cfg.StateDir, "state-dir", "/var/lib/perch-demo", "session state directory")
	flag.StringVar(&cfg.DockerBin, "docker-bin", "docker", "docker binary")
	flag.BoolVar(&cfg.SecureCookie, "secure-cookie", true, "set the Secure attribute on the session cookie")
	flag.StringVar(&cfg.TurnstileSiteKey, "turnstile-site-key", os.Getenv("TURNSTILE_SITE_KEY"), "Cloudflare Turnstile site key (empty disables verification)")
	flag.StringVar(&cfg.TurnstileSecret, "turnstile-secret", os.Getenv("TURNSTILE_SECRET_KEY"), "Cloudflare Turnstile secret key")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if (cfg.TurnstileSiteKey == "") != (cfg.TurnstileSecret == "") {
		log.Error("turnstile requires both a site key and a secret key")
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "sessions"), 0o700); err != nil {
		log.Error("create state directory", "error", err)
		os.Exit(1)
	}
	srv := &server{
		cfg: cfg,
		log: log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				u, err := url.Parse(origin)
				return err == nil && u.Host == r.Host
			},
		},
		sessions:   make(map[string]*session),
		byIP:       make(map[string]map[string]struct{}),
		creates:    make(map[string][]time.Time),
		verifies:   make(map[string][]time.Time),
		createSem:  make(chan struct{}, 3),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.cleanupStartup(ctx)
	go srv.cleanupLoop(ctx)

	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Error("prepare web assets", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(webRoot))))
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/config", srv.handleConfig)
	mux.HandleFunc("/api/session", srv.handleSession)
	mux.HandleFunc("/ws", srv.handleTerminal)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Info("previewd listening", "addr", cfg.Listen, "domain", cfg.Domain, "ttl", cfg.TTL,
		"turnstile", srv.turnstileEnabled())
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "error", err)
		os.Exit(1)
	}
	srv.cleanupAll()
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "missing web assets", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"turnstile_site_key": s.cfg.TurnstileSiteKey,
		"turnstile_required": s.turnstileEnabled(),
	})
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sess := s.currentSession(r)
		if sess == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, s.view(sess))
	case http.MethodPost:
		if sess := s.currentSession(r); sess != nil {
			writeJSON(w, http.StatusOK, s.view(sess))
			return
		}
		if s.turnstileEnabled() {
			ip := clientIP(r)
			if !s.allowVerify(ip, time.Now().UTC()) {
				http.Error(w, "too many verification attempts; try again later", http.StatusTooManyRequests)
				return
			}
			token, err := turnstileTokenFrom(w, r)
			if err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if err := s.verifyTurnstile(r.Context(), token, ip); err != nil {
				status := http.StatusForbidden
				if errors.Is(err, errTurnstileUnavailable) {
					status = http.StatusServiceUnavailable
				}
				http.Error(w, err.Error(), status)
				return
			}
		}
		sess, err := s.createSession(r)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, errTooMany), errors.Is(err, errRateLimited):
				status = http.StatusTooManyRequests
			}
			http.Error(w, err.Error(), status)
			return
		}
		s.setCookie(w, r, sess.ID, int(time.Until(sess.Expires).Seconds()))
		writeJSON(w, http.StatusCreated, s.view(sess))
	case http.MethodDelete:
		sess := s.currentSession(r)
		if sess != nil {
			s.endSession(context.Background(), sess, "requested")
		}
		s.clearCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

var (
	errTooMany              = errors.New("demo is full; try again shortly")
	errRateLimited          = errors.New("too many demo sessions from this address; try again later")
	errTurnstileRejected    = errors.New("verification failed; try again")
	errTurnstileUnavailable = errors.New("verification unavailable; try again shortly")
)

const maxActivePerIP = 3
const maxCreatesPerHour = 10
const maxVerifiesPerHour = 30

const (
	turnstileAction       = "demo_session"
	defaultTurnstileURL   = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	turnstileMaxBodyBytes = 4 << 10
)

func (s *server) turnstileEnabled() bool {
	return s.cfg.TurnstileSiteKey != "" && s.cfg.TurnstileSecret != ""
}

func (s *server) turnstileVerifyURL() string {
	if s.cfg.turnstileURL != "" {
		return s.cfg.turnstileURL
	}
	return defaultTurnstileURL
}

func (s *server) allowVerify(ip string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLimitsLocked(now)
	if len(s.verifies[ip]) >= maxVerifiesPerHour {
		return false
	}
	s.verifies[ip] = append(s.verifies[ip], now)
	return true
}

func turnstileTokenFrom(w http.ResponseWriter, r *http.Request) (string, error) {
	var body struct {
		TurnstileToken string `json:"turnstile_token"`
	}
	if r.Body == nil {
		return "", nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, turnstileMaxBodyBytes))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(body.TurnstileToken), nil
}

func (s *server) verifyTurnstile(ctx context.Context, token, remoteIP string) error {
	if !s.turnstileEnabled() {
		return nil
	}
	if token == "" {
		return errTurnstileRejected
	}
	form := url.Values{}
	form.Set("secret", s.cfg.TurnstileSecret)
	form.Set("response", token)
	if ip := net.ParseIP(remoteIP); ip != nil {
		form.Set("remoteip", ip.String())
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.turnstileVerifyURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return errTurnstileUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		s.log.Warn("turnstile verify request", "error", err, "ip", remoteIP)
		return errTurnstileUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("turnstile verify status", "status", resp.StatusCode, "ip", remoteIP)
		return errTurnstileUnavailable
	}
	var result struct {
		Success    bool     `json:"success"`
		Action     string   `json:"action"`
		Hostname   string   `json:"hostname"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		s.log.Warn("turnstile verify response", "error", err, "ip", remoteIP)
		return errTurnstileUnavailable
	}
	if !result.Success || result.Action != turnstileAction {
		s.log.Warn("turnstile rejected", "codes", result.ErrorCodes, "action", result.Action,
			"hostname", result.Hostname, "ip", remoteIP)
		return errTurnstileRejected
	}
	return nil
}

func (s *server) createSession(r *http.Request) (*session, error) {
	ip := clientIP(r)
	now := time.Now().UTC()

	s.mu.Lock()
	s.pruneLimitsLocked(now)
	if len(s.sessions)+s.creating >= s.cfg.MaxSessions {
		s.mu.Unlock()
		return nil, errTooMany
	}
	if len(s.byIP[ip]) >= maxActivePerIP || len(s.creates[ip]) >= maxCreatesPerHour {
		s.mu.Unlock()
		return nil, errRateLimited
	}
	s.creating++
	s.creates[ip] = append(s.creates[ip], now)
	s.mu.Unlock()

	release := func() {
		s.mu.Lock()
		s.creating--
		s.mu.Unlock()
	}
	defer release()

	select {
	case s.createSem <- struct{}{}:
		defer func() { <-s.createSem }()
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}

	id := randomHex(8)
	address := "demo-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes(8))) + "@" + s.cfg.Domain
	password := base64.RawURLEncoding.EncodeToString(randomBytes(18))
	dir := filepath.Join(s.cfg.StateDir, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
	}
	if err := writeTerminalConfig(filepath.Join(dir, "perch.yaml"), s.cfg); err != nil {
		cleanup()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := s.createAccount(ctx, address, password); err != nil {
		cleanup()
		return nil, err
	}
	containerID, err := s.startContainer(ctx, id, dir, address, password)
	if err != nil {
		_ = s.deleteAccount(ctx, address)
		cleanup()
		return nil, err
	}
	sess := &session{
		ID: id, Address: address, Password: password,
		Created: now, Expires: now.Add(s.cfg.TTL),
		ContainerID: containerID, Dir: dir, IP: ip,
	}

	s.mu.Lock()
	if len(s.sessions) >= s.cfg.MaxSessions {
		s.mu.Unlock()
		s.endSession(context.Background(), sess, "capacity")
		return nil, errTooMany
	}
	s.sessions[id] = sess
	if s.byIP[ip] == nil {
		s.byIP[ip] = make(map[string]struct{})
	}
	s.byIP[ip][id] = struct{}{}
	s.mu.Unlock()
	s.log.Info("demo session started", "session", id, "address", address, "ip", ip, "expires", sess.Expires)
	return sess, nil
}

func writeTerminalConfig(path string, cfg config) error {
	data := fmt.Sprintf(`data_dir: /tmp/perch
hostname: %s
imap:
  addr: ""
  imaps_addr: %s:%d
  allow_plaintext_loopback: false
submission:
  addr: ""
  tls_addr: %s:%d
storage:
  mode: sqlite
limits:
  max_message_bytes: 10485760
  max_recipients: 5
`, cfg.MailHost, cfg.MailHost, cfg.IMAPPort, cfg.MailHost, cfg.SMTPPort)
	return os.WriteFile(path, []byte(data), 0o444)
}

func (s *server) createAccount(ctx context.Context, address, password string) error {
	cmd := exec.CommandContext(ctx, s.cfg.PerchBin, "account", "add",
		"--config", s.cfg.PerchConfig, "--email", address, "--name", "perch live demo", "--password", password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.log.Error("create account", "address", address, "error", err, "output", strings.TrimSpace(string(out)))
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}

func (s *server) deleteAccount(ctx context.Context, address string) error {
	cmd := exec.CommandContext(ctx, s.cfg.PerchBin, "account", "delete",
		"--config", s.cfg.PerchConfig, "--email", address, "--yes")
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.log.Warn("delete account", "address", address, "error", err, "output", strings.TrimSpace(string(out)))
		return err
	}
	return nil
}

func (s *server) startContainer(ctx context.Context, id, dir, address, password string) (string, error) {
	name := "perch-demo-" + id
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--label", "perch-demo=1",
		"--label", "perch-demo-session=" + id,
		"--network", s.cfg.Network,
		"--read-only",
		"--tmpfs", "/tmp:rw,size=32m,noexec,nosuid",
		"--memory", "128m", "--memory-swap", "128m",
		"--cpus", "0.25", "--pids-limit", "64",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--add-host", s.cfg.MailHost + ":" + s.cfg.ProxyIP,
		"-v", filepath.Join(dir, "perch.yaml") + ":/demo/perch.yaml:ro",
		"-e", "PERCH_CONFIG=/demo/perch.yaml",
		"-e", "PERCH_IMAP_USER=" + address,
		"-e", "PERCH_IMAP_PASSWORD=" + password,
		s.cfg.Image, "sleep", "infinity",
	}
	cmd := exec.CommandContext(ctx, s.cfg.DockerBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.log.Error("start terminal container", "session", id, "error", err, "output", strings.TrimSpace(string(out)))
		return "", fmt.Errorf("start terminal: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	sess := s.currentSession(r)
	if sess == nil {
		http.Error(w, "no demo session", http.StatusUnauthorized)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(64 * 1024)

	cmd := exec.Command(s.cfg.DockerBin, "exec", "-i", "-t", "-e", "TERM=xterm-256color", sess.ContainerID, "/bin/sh", "-c", "stty -echo 2>/dev/null; exec /bin/sh")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("terminal unavailable\r\n"))
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	type input struct {
		Type string `json:"type"`
		Data string `json:"data"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg input
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			_, _ = io.WriteString(ptmx, msg.Data)
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
			}
		}
	}
	_ = conn.Close()
	<-done
}

func (s *server) currentSession(r *http.Request) *session {
	cookie, err := r.Cookie("perch_demo")
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[cookie.Value]
	if sess == nil || time.Now().After(sess.Expires) {
		return nil
	}
	return sess
}

func (s *server) setCookie(w http.ResponseWriter, r *http.Request, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: "perch_demo", Value: id, Path: "/", HttpOnly: true,
		Secure:   s.cfg.SecureCookie || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func (s *server) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "perch_demo", Value: "", Path: "/", HttpOnly: true,
		Secure:   s.cfg.SecureCookie || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (s *server) view(sess *session) sessionView {
	return sessionView{
		Address: sess.Address, Password: sess.Password,
		ExpiresAt: sess.Expires, TTLSeconds: int(time.Until(sess.Expires).Seconds()),
		IMAPHost: s.cfg.MailHost, IMAPPort: s.cfg.IMAPPort,
		SMTPHost: s.cfg.MailHost, SMTPPort: s.cfg.SMTPPort,
	}
}

func (s *server) endSession(ctx context.Context, sess *session, reason string) {
	s.mu.Lock()
	delete(s.sessions, sess.ID)
	if set := s.byIP[sess.IP]; set != nil {
		delete(set, sess.ID)
		if len(set) == 0 {
			delete(s.byIP, sess.IP)
		}
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, s.cfg.DockerBin, "rm", "-f", sess.ContainerID).Run()
	_ = s.deleteAccount(ctx, sess.Address)
	_ = os.RemoveAll(sess.Dir)
	s.log.Info("demo session ended", "session", sess.ID, "address", sess.Address, "reason", reason)
}

func (s *server) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			var expired []*session
			s.mu.Lock()
			s.pruneLimitsLocked(now)
			for id, sess := range s.sessions {
				if now.After(sess.Expires) {
					expired = append(expired, sess)
					delete(s.sessions, id)
					if set := s.byIP[sess.IP]; set != nil {
						delete(set, id)
					}
				}
			}
			s.mu.Unlock()
			for _, sess := range expired {
				s.endSession(context.Background(), sess, "expired")
			}
		}
	}
}

func (s *server) pruneLimitsLocked(now time.Time) {
	cutoff := now.Add(-time.Hour)
	for _, bucket := range []map[string][]time.Time{s.creates, s.verifies} {
		for ip, times := range bucket {
			kept := times[:0]
			for _, t := range times {
				if t.After(cutoff) {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				delete(bucket, ip)
			} else {
				bucket[ip] = kept
			}
		}
	}
}

func (s *server) cleanupStartup(ctx context.Context) {
	s.log.Info("cleaning up previous demo sessions")
	out, _ := exec.CommandContext(ctx, s.cfg.DockerBin, "ps", "-aq", "--filter", "label=perch-demo=1").Output()
	for _, id := range strings.Fields(string(out)) {
		_ = exec.CommandContext(ctx, s.cfg.DockerBin, "rm", "-f", id).Run()
	}
	if err := os.RemoveAll(filepath.Join(s.cfg.StateDir, "sessions")); err != nil {
		s.log.Warn("remove session directory", "error", err)
	}
	_ = os.MkdirAll(filepath.Join(s.cfg.StateDir, "sessions"), 0o700)
	out, err := exec.CommandContext(ctx, s.cfg.PerchBin, "account", "list", "--config", s.cfg.PerchConfig).Output()
	if err != nil {
		s.log.Warn("list accounts for cleanup", "error", err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "demo-") || !strings.HasSuffix(fields[1], "@"+s.cfg.Domain) {
			continue
		}
		_ = s.deleteAccount(ctx, fields[1])
	}
}

func (s *server) cleanupAll() {
	s.mu.Lock()
	list := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	s.mu.Unlock()
	for _, sess := range list {
		s.endSession(context.Background(), sess, "shutdown")
	}
}

func clientIP(r *http.Request) string {
	// nginx sets X-Real-IP to the direct peer. Prefer it over the
	// client-supplied X-Forwarded-For chain so rate limits and Turnstile see
	// the real address.
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func randomHex(n int) string {
	return hex.EncodeToString(randomBytes(n))
}
