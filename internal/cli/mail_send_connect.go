package cli

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"strings"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/smtpclient"
	cli "github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func smtpConnectionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "smtp-server",
			Aliases: []string{"S"},
			Usage:   "SMTP submission server (smtps://host, smtp://host, or host[:port]); defaults from --config",
		},
		&cli.BoolFlag{Name: "smtp-plaintext", Usage: "connect to SMTP without TLS (use only on trusted networks)"},
		&cli.BoolFlag{Name: "smtp-starttls", Usage: "require STARTTLS for SMTP"},
		&cli.BoolFlag{Name: "smtp-implicit-tls", Usage: "use implicit TLS for SMTP (usually port 465)"},
		&cli.BoolFlag{Name: "smtp-insecure", Usage: "skip SMTP TLS certificate verification"},
		&cli.StringFlag{Name: "smtp-ca-file", Usage: "PEM CA bundle used to verify the SMTP server certificate"},
	}
}

func smtpClientOptions(cmd *cli.Command, authenticate bool) (smtpclient.Options, error) {
	mode := smtpclient.TLSStartTLS
	modeSet := false
	if countTrue(cmd, "smtp-plaintext", "smtp-starttls", "smtp-implicit-tls") > 1 {
		return smtpclient.Options{}, errors.New("--smtp-plaintext, --smtp-starttls, and --smtp-implicit-tls are mutually exclusive")
	}
	if cmd.Bool("smtp-plaintext") {
		mode = smtpclient.TLSDisabled
		modeSet = true
	}
	if cmd.Bool("smtp-starttls") {
		mode = smtpclient.TLSStartTLS
		modeSet = true
	}
	if cmd.Bool("smtp-implicit-tls") {
		mode = smtpclient.TLSImplicit
		modeSet = true
	}

	localName := "localhost"
	addr := strings.TrimSpace(cmd.String("smtp-server"))
	if addr == "" {
		cfg, err := config.Load(cmd.String("config"))
		if err != nil {
			return smtpclient.Options{}, fmt.Errorf("--smtp-server is required when no configuration is available: %w", err)
		}
		if cfg.Hostname != "" {
			localName = cfg.Hostname
		}
		switch {
		case modeSet && mode == smtpclient.TLSImplicit:
			if cfg.SMTP.TLSAddr != "" {
				addr = cfg.SMTP.TLSAddr
			} else if cfg.SMTP.Addr != "" {
				addr = cfg.SMTP.Addr
			}
		case modeSet:
			addr = cfg.SMTP.Addr
		case cfg.SMTP.TLSAddr != "":
			addr = cfg.SMTP.TLSAddr
			mode = smtpclient.TLSImplicit
		case cfg.SMTP.Addr != "":
			addr = cfg.SMTP.Addr
			if cfg.TLS.CertFile != "" || cfg.TLS.ACME.Enabled {
				mode = smtpclient.TLSStartTLS
			} else {
				mode = smtpclient.TLSDisabled
			}
		}
		if addr == "" {
			if modeSet {
				return smtpclient.Options{}, errors.New("configuration has no submission listener for the requested SMTP TLS mode; pass --smtp-server")
			}
			return smtpclient.Options{}, errors.New("no submission listener is configured; pass --smtp-server")
		}
	} else {
		schemeMode, host, hasScheme, err := parseSMTPServerAddress(addr)
		if err != nil {
			return smtpclient.Options{}, err
		}
		if hasScheme {
			if modeSet && mode != schemeMode {
				return smtpclient.Options{}, errors.New("SMTP server URL scheme conflicts with --smtp-plaintext/--smtp-starttls/--smtp-implicit-tls")
			}
			mode = schemeMode
		} else if !modeSet && hasPort(host, "465") {
			mode = smtpclient.TLSImplicit
		}
		addr = host
	}
	addr = normalizeSMTPAddress(addr, mode)

	username, password := "", ""
	if authenticate {
		var err error
		username, password, err = smtpCredentials(cmd)
		if err != nil {
			return smtpclient.Options{}, err
		}
	}
	tlsConfig, err := smtpTLSConfig(cmd, addr, mode)
	if err != nil {
		return smtpclient.Options{}, err
	}
	return smtpclient.Options{
		Addr:      addr,
		TLSMode:   mode,
		TLSConfig: tlsConfig,
		Username:  username,
		Password:  password,
		LocalName: localName,
		Timeout:   cmd.Duration("timeout"),
	}, nil
}

func parseSMTPServerAddress(value string) (smtpclient.TLSMode, string, bool, error) {
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "smtps://"):
		host := strings.TrimSuffix(value[len("smtps://"):], "/")
		if strings.Contains(host, "/") {
			return 0, "", true, fmt.Errorf("SMTP server URL must not include a path: %q", value)
		}
		return smtpclient.TLSImplicit, host, true, nil
	case strings.HasPrefix(lower, "smtp://"):
		host := strings.TrimSuffix(value[len("smtp://"):], "/")
		if strings.Contains(host, "/") {
			return 0, "", true, fmt.Errorf("SMTP server URL must not include a path: %q", value)
		}
		return smtpclient.TLSStartTLS, host, true, nil
	case strings.Contains(value, "://"):
		return 0, "", true, fmt.Errorf("unsupported SMTP URL scheme in %q (use smtp:// or smtps://)", value)
	default:
		return smtpclient.TLSStartTLS, value, false, nil
	}
}

func normalizeSMTPAddress(host string, mode smtpclient.TLSMode) string {
	host = strings.TrimSpace(strings.TrimSuffix(host, "/"))
	port := ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		host, port = h, p
	} else {
		host = strings.TrimPrefix(host, "[")
		host = strings.TrimSuffix(host, "]")
	}
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "587"
		if mode == smtpclient.TLSImplicit {
			port = "465"
		}
	}
	return net.JoinHostPort(host, port)
}

func hasPort(addr, want string) bool {
	_, port, err := net.SplitHostPort(addr)
	return err == nil && port == want
}

func smtpCredentials(cmd *cli.Command) (string, string, error) {
	username := strings.TrimSpace(cmd.String("username"))
	if username == "" {
		username = strings.TrimSpace(os.Getenv("PERCH_IMAP_USER"))
	}
	explicitPassword := cmd.IsSet("password") || cmd.IsSet("password-file") || cmd.IsSet("password-stdin")
	passwordAvailable := explicitPassword || os.Getenv("PERCH_IMAP_PASSWORD") != "" ||
		term.IsTerminal(int(os.Stdin.Fd()))
	if username == "" && passwordAvailable {
		// Default the SMTP username to the sender address when credentials are
		// supplied but the user did not repeat the account address.
		if address, err := mail.ParseAddress(strings.TrimSpace(cmd.String("from"))); err == nil {
			username = address.Address
		}
	}
	if username == "" {
		if explicitPassword {
			return "", "", errors.New("--username is required when a password is set")
		}
		return "", "", nil
	}
	password, err := mailPassword(cmd)
	if err != nil {
		return "", "", err
	}
	return username, password, nil
}

func smtpTLSConfig(cmd *cli.Command, addr string, mode smtpclient.TLSMode) (*tls.Config, error) {
	if mode == smtpclient.TLSDisabled {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parse SMTP server address: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	}
	if cmd.Bool("smtp-insecure") {
		tlsConfig.InsecureSkipVerify = true
	}
	if path := cmd.String("smtp-ca-file"); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read SMTP CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", path)
		}
		tlsConfig.RootCAs = pool
	}
	return tlsConfig, nil
}

func countTrue(cmd *cli.Command, names ...string) int {
	count := 0
	for _, name := range names {
		if cmd.Bool(name) {
			count++
		}
	}
	return count
}
