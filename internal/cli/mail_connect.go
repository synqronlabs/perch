package cli

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/mailclient"
	cli "github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func openMailClient(cmd *cli.Command) (*mailclient.Client, error) {
	opts, err := mailClientOptions(cmd)
	if err != nil {
		return nil, err
	}
	client, err := mailclient.Dial(opts)
	if err != nil {
		if opts.TLSMode == mailclient.TLSImplicit && isLoopbackAddress(opts.Addr) &&
			(strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "handshake")) {
			return nil, fmt.Errorf("%w (hint: use --plaintext for a loopback plaintext listener)", err)
		}
		return nil, err
	}
	return client, nil
}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func mailClientOptions(cmd *cli.Command) (mailclient.Options, error) {
	mode := mailclient.TLSImplicit
	modeSet := false
	if cmd.Bool("plaintext") && cmd.Bool("starttls") {
		return mailclient.Options{}, errors.New("--plaintext and --starttls are mutually exclusive")
	}
	if cmd.Bool("plaintext") {
		mode = mailclient.TLSDisabled
		modeSet = true
	}
	if cmd.Bool("starttls") {
		mode = mailclient.TLSStartTLS
		modeSet = true
	}

	addr := strings.TrimSpace(cmd.String("server"))
	if addr == "" {
		cfg, err := config.Load(cmd.String("config"))
		if err != nil {
			return mailclient.Options{}, fmt.Errorf("--server is required when no configuration is available: %w", err)
		}
		switch {
		case modeSet:
			// An explicit plaintext/STARTTLS choice refers to the plaintext
			// listener, not the implicit-TLS one.
			if cfg.IMAP.Addr == "" {
				return mailclient.Options{}, errors.New("configuration has no imap.addr for --plaintext/--starttls; pass --server")
			}
			addr = cfg.IMAP.Addr
		case cfg.IMAP.IMAPSAddr != "":
			addr = cfg.IMAP.IMAPSAddr
			mode = mailclient.TLSImplicit
		case cfg.IMAP.Addr != "":
			addr = cfg.IMAP.Addr
			if cfg.TLS.CertFile != "" || cfg.TLS.ACME.Enabled {
				mode = mailclient.TLSStartTLS
			} else {
				mode = mailclient.TLSDisabled
			}
		default:
			return mailclient.Options{}, errors.New("no IMAP listener is configured; pass --server")
		}
	} else {
		schemeMode, host, hasScheme, err := parseServerAddress(addr)
		if err != nil {
			return mailclient.Options{}, err
		}
		if hasScheme {
			if modeSet && mode != schemeMode {
				return mailclient.Options{}, errors.New("server URL scheme conflicts with --plaintext/--starttls")
			}
			mode = schemeMode
		}
		addr = host
	}
	addr = normalizeServerAddress(addr, mode)

	username, err := mailUsername(cmd)
	if err != nil {
		return mailclient.Options{}, err
	}
	password, err := mailPassword(cmd)
	if err != nil {
		return mailclient.Options{}, err
	}
	tlsConfig, err := mailTLSConfig(cmd, addr, mode)
	if err != nil {
		return mailclient.Options{}, err
	}
	return mailclient.Options{
		Addr:      addr,
		TLSMode:   mode,
		TLSConfig: tlsConfig,
		Username:  username,
		Password:  password,
		Timeout:   cmd.Duration("timeout"),
	}, nil
}

func parseServerAddress(value string) (mailclient.TLSMode, string, bool, error) {
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "imaps://"):
		host := strings.TrimSuffix(value[len("imaps://"):], "/")
		if strings.Contains(host, "/") {
			return 0, "", true, fmt.Errorf("server URL must not include a path: %q", value)
		}
		return mailclient.TLSImplicit, host, true, nil
	case strings.HasPrefix(lower, "imap://"):
		host := strings.TrimSuffix(value[len("imap://"):], "/")
		if strings.Contains(host, "/") {
			return 0, "", true, fmt.Errorf("server URL must not include a path: %q", value)
		}
		return mailclient.TLSStartTLS, host, true, nil
	case strings.Contains(value, "://"):
		return 0, "", true, fmt.Errorf("unsupported server URL scheme in %q (use imap:// or imaps://)", value)
	default:
		return mailclient.TLSImplicit, value, false, nil
	}
}

func normalizeServerAddress(host string, mode mailclient.TLSMode) string {
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
		port = "143"
		if mode == mailclient.TLSImplicit {
			port = "993"
		}
	}
	return net.JoinHostPort(host, port)
}

func mailUsername(cmd *cli.Command) (string, error) {
	if value := strings.TrimSpace(cmd.String("username")); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(os.Getenv("PERCH_IMAP_USER")); value != "" {
		return value, nil
	}
	return "", errors.New("--username is required (or set PERCH_IMAP_USER)")
}

func mailPassword(cmd *cli.Command) (string, error) {
	methods := 0
	for _, name := range []string{"password", "password-file", "password-stdin"} {
		if cmd.IsSet(name) {
			methods++
		}
	}
	if methods > 1 {
		return "", errors.New("use only one of --password, --password-file, and --password-stdin")
	}
	switch {
	case cmd.IsSet("password"):
		return cmd.String("password"), nil
	case cmd.IsSet("password-file"):
		data, err := os.ReadFile(cmd.String("password-file"))
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	case cmd.IsSet("password-stdin"):
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	if value := os.Getenv("PERCH_IMAP_PASSWORD"); value != "" {
		return value, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return promptPassword("Password: ")
	}
	return "", errors.New("no password: use --password-file, --password-stdin, or PERCH_IMAP_PASSWORD")
}

func mailTLSConfig(cmd *cli.Command, addr string, mode mailclient.TLSMode) (*tls.Config, error) {
	if mode == mailclient.TLSDisabled {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parse server address: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	}
	if cmd.Bool("insecure") {
		tlsConfig.InsecureSkipVerify = true
	}
	if path := cmd.String("ca-file"); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
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
