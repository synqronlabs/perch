// Package config loads and validates the perch configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// StorageMode selects where raw messages are kept.
type StorageMode string

const (
	// StorageSQLite keeps compressed raw messages inside the SQLite database.
	StorageSQLite StorageMode = "sqlite"
	// StorageS3 keeps raw messages in an S3 bucket populated by mxRaven's
	// S3_STORE action while metadata and search indexes stay in SQLite.
	StorageS3 StorageMode = "s3"
)

// Config is the top-level perch configuration.
type Config struct {
	DataDir  string        `yaml:"data_dir"`
	Hostname string        `yaml:"hostname"`
	Log      LogConfig     `yaml:"log"`
	IMAP     IMAPConfig    `yaml:"imap"`
	SMTP     SMTPConfig    `yaml:"submission"`
	TLS      TLSConfig     `yaml:"tls"`
	Storage  StorageConfig `yaml:"storage"`
	MXRaVen  MXRaVenConfig `yaml:"mxraven"`
	Limits   LimitsConfig  `yaml:"limits"`
	Setup    SetupConfig   `yaml:"setup"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json or text
}

// IMAPConfig configures the IMAP listeners.
type IMAPConfig struct {
	Addr                   string `yaml:"addr"`
	IMAPSAddr              string `yaml:"imaps_addr"`
	AllowPlaintextLoopback bool   `yaml:"allow_plaintext_loopback"`
}

// SMTPConfig configures the submission listeners.
type SMTPConfig struct {
	Addr    string `yaml:"addr"`
	TLSAddr string `yaml:"tls_addr"`
}

// TLSConfig enables TLS for IMAP and submission.
type TLSConfig struct {
	CertFile string     `yaml:"cert_file"`
	KeyFile  string     `yaml:"key_file"`
	ACME     ACMEConfig `yaml:"acme"`
}

// ACMEConfig configures built-in Let's Encrypt issuance.
type ACMEConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Email    string   `yaml:"email"`
	Domains  []string `yaml:"domains"`
	CacheDir string   `yaml:"cache_dir"`
	HTTPAddr string   `yaml:"http_addr"`
}

// StorageConfig selects the raw-message store.
type StorageConfig struct {
	Mode StorageMode `yaml:"mode"`
	S3   S3Config    `yaml:"s3"`
}

// S3Config describes the bucket mxRaven writes to and perch reads from.
type S3Config struct {
	Endpoint       string        `yaml:"endpoint"`
	Region         string        `yaml:"region"`
	Bucket         string        `yaml:"bucket"`
	Prefix         string        `yaml:"prefix"`
	AccessKey      string        `yaml:"access_key"`
	SecretKey      string        `yaml:"secret_key"`
	SecretKeyEnv   string        `yaml:"secret_key_env"`
	ForcePathStyle bool          `yaml:"force_path_style"`
	KeyTemplate    string        `yaml:"key_template"`
	ReconcileEvery time.Duration `yaml:"reconcile_every"`
	Spool          bool          `yaml:"spool"`
	SpoolMaxBytes  int64         `yaml:"spool_max_bytes"`
}

// Secret returns the S3 secret from the environment when configured.
func (c S3Config) Secret() string {
	if c.SecretKeyEnv != "" {
		if value := os.Getenv(c.SecretKeyEnv); value != "" {
			return value
		}
	}
	return c.SecretKey
}

// MXRaVenConfig configures the upstream mail transport.
type MXRaVenConfig struct {
	SubmissionHost  string           `yaml:"submission_host"`
	SubmissionPort  int              `yaml:"submission_port"`
	NotificationURL string           `yaml:"notification_url"`
	FeedbackURL     string           `yaml:"feedback_url"`
	Listeners       []ListenerConfig `yaml:"listeners"`
}

// ListenerConfig identifies one mxRaven listener and its API key.
type ListenerConfig struct {
	ID           string `yaml:"id"`
	Kind         string `yaml:"kind"` // mta or submission
	KeyPrefix    string `yaml:"key_prefix"`
	KeySecret    string `yaml:"key_secret"`
	KeySecretEnv string `yaml:"key_secret_env"`
	Consumer     string `yaml:"consumer"`
}

// Secret returns the listener secret from the environment when configured.
func (l ListenerConfig) Secret() string {
	if l.KeySecretEnv != "" {
		if value := os.Getenv(l.KeySecretEnv); value != "" {
			return value
		}
	}
	return l.KeySecret
}

// LimitsConfig bounds resource use.
type LimitsConfig struct {
	MaxMessageBytes   int64 `yaml:"max_message_bytes"`
	MaxRecipients     int   `yaml:"max_recipients"`
	MaxConnections    int   `yaml:"max_connections"`
	IngestConcurrency int   `yaml:"ingest_concurrency"`
	SearchMaxMessages int   `yaml:"search_max_messages"`
	BlobCacheBytes    int64 `yaml:"blob_cache_bytes"`
}

// SetupConfig records values written by the setup wizard for diagnostics.
type SetupConfig struct {
	Domain        string `yaml:"domain"`
	InboundDomain string `yaml:"inbound_domain"`
	SendingDomain string `yaml:"sending_domain"`
	MTAListenerID string `yaml:"mta_listener_id"`
	SubListenerID string `yaml:"submission_listener_id"`
	StorageRef    string `yaml:"storage_ref"`
}

// Default returns the configuration used by a fresh local installation.
func Default() Config {
	return Config{
		DataDir:  defaultDataDir(),
		Hostname: "localhost",
		Log:      LogConfig{Level: "info", Format: "text"},
		IMAP: IMAPConfig{
			Addr:                   "127.0.0.1:1143",
			IMAPSAddr:              "",
			AllowPlaintextLoopback: true,
		},
		SMTP: SMTPConfig{
			Addr: "127.0.0.1:1587",
		},
		Storage: StorageConfig{Mode: StorageSQLite, S3: S3Config{
			ReconcileEvery: 5 * time.Minute,
			SpoolMaxBytes:  256 << 20,
		}},
		MXRaVen: MXRaVenConfig{
			SubmissionHost:  "smtp.mxraven.email",
			SubmissionPort:  587,
			NotificationURL: "wss://notifications.mxraven.com",
			FeedbackURL:     "https://api.mxraven.com",
		},
		Limits: LimitsConfig{
			MaxMessageBytes:   25 << 20,
			MaxRecipients:     100,
			MaxConnections:    512,
			IngestConcurrency: 2,
			SearchMaxMessages: 50000,
			BlobCacheBytes:    64 << 20,
		},
	}
}

func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "perch")
	}
	return "./perch-data"
}

// Load reads path into a Config, applies environment overrides, and validates
// it. A missing file is an error: perch never starts with an implicit config.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("PERCH_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("PERCH_HOSTNAME"); v != "" {
		c.Hostname = v
	}
}

// Validate checks the invariants that would otherwise fail at runtime.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data_dir is required")
	}
	if strings.TrimSpace(c.Hostname) == "" {
		return errors.New("hostname is required")
	}
	switch c.Storage.Mode {
	case StorageSQLite:
	case StorageS3:
		if c.Storage.S3.Bucket == "" {
			return errors.New("storage.s3.bucket is required in s3 mode")
		}
		if c.Storage.S3.KeyTemplate == "" {
			return errors.New("storage.s3.key_template is required in s3 mode")
		}
		if !validS3KeyTemplate(c.Storage.S3.KeyTemplate) {
			return errors.New("storage.s3.key_template must include {{.task.id}}, end with .eml, and identify a mailbox address")
		}
		if c.Storage.S3.Prefix != "" && !strings.HasPrefix(c.Storage.S3.KeyTemplate, c.Storage.S3.Prefix) {
			return errors.New("storage.s3.key_template must start with storage.s3.prefix so reconciliation can list the same keys")
		}
		if c.Storage.S3.Secret() == "" {
			return errors.New("storage.s3.secret_key or secret_key_env is required in s3 mode")
		}
	default:
		return fmt.Errorf("unknown storage mode %q", c.Storage.Mode)
	}
	if c.TLS.CertFile != "" && c.TLS.KeyFile == "" || c.TLS.KeyFile != "" && c.TLS.CertFile == "" {
		return errors.New("tls.cert_file and tls.key_file must be set together")
	}
	if c.TLS.ACME.Enabled && len(c.TLS.ACME.Domains) == 0 {
		return errors.New("tls.acme.domains is required when ACME is enabled")
	}
	for i, listener := range c.MXRaVen.Listeners {
		if listener.ID == "" {
			return fmt.Errorf("mxraven.listeners[%d].id is required", i)
		}
		if listener.KeyPrefix == "" || listener.Secret() == "" {
			return fmt.Errorf("mxraven.listeners[%d] requires key_prefix and a secret", i)
		}
		switch listener.Kind {
		case "mta", "submission":
		default:
			return fmt.Errorf("mxraven.listeners[%d].kind must be mta or submission", i)
		}
	}
	if c.IMAP.Addr == "" && c.IMAP.IMAPSAddr == "" && c.SMTP.Addr == "" && c.SMTP.TLSAddr == "" {
		return errors.New("at least one listener address is required")
	}
	if c.Limits.MaxMessageBytes <= 0 {
		return errors.New("limits.max_message_bytes must be positive")
	}
	if c.Limits.MaxRecipients <= 0 {
		return errors.New("limits.max_recipients must be positive")
	}
	return nil
}

// Save writes cfg to path with owner-only permissions.
func Save(path string, cfg Config) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// DBPath returns the SQLite database path.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "perch.db")
}

// EnsureDataDir creates the private data directory.
func (c Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return nil
}

// Listener returns the first configured listener of kind, if any.
func (c Config) Listener(kind string) (ListenerConfig, bool) {
	for _, listener := range c.MXRaVen.Listeners {
		if listener.Kind == kind {
			return listener, true
		}
	}
	return ListenerConfig{}, false
}

func validS3KeyTemplate(template string) bool {
	if !strings.Contains(template, "{{.task.id}}") || !strings.HasSuffix(strings.ToLower(template), ".eml") {
		return false
	}
	return strings.Contains(template, "{{.mailbox.address}}") ||
		(strings.Contains(template, "{{.mailbox.local_part}}") && strings.Contains(template, "{{.mailbox.domain}}"))
}
