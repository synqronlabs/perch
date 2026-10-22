package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
	"github.com/synqronlabs/perch/internal/tlsutil"
	cli "github.com/urfave/cli/v3"
)

func initCommand() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "create a local configuration and optional first account",
		Flags: []cli.Flag{
			configFlag(""),
			&cli.StringFlag{Name: "data-dir", Usage: "data directory"},
			&cli.StringFlag{Name: "hostname", Value: "localhost", Usage: "server hostname"},
			&cli.StringFlag{Name: "email", Usage: "first account email"},
			&cli.StringFlag{Name: "name", Usage: "first account display name"},
			&cli.StringFlag{Name: "password", Usage: "first account password (prompted when empty)"},
			&cli.BoolFlag{Name: "self-signed", Usage: "generate a self-signed TLS certificate"},
			&cli.BoolFlag{Name: "force", Usage: "overwrite an existing configuration"},
		},
		Action: runInit,
	}
}

func runInit(ctx context.Context, cmd *cli.Command) error {
	dir := cmd.String("data-dir")
	if dir == "" {
		dir = config.Default().DataDir
	}
	path := cmd.String("config")
	if path == "" {
		path = filepath.Join(dir, "perch.yaml")
	}
	if !cmd.Bool("force") {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("configuration %q already exists (use --force)", path)
		}
	}
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Hostname = cmd.String("hostname")
	if cmd.Bool("self-signed") {
		cert := filepath.Join(dir, "tls", "cert.pem")
		key := filepath.Join(dir, "tls", "key.pem")
		if err := tlsutil.GenerateSelfSigned(cert, key, cfg.Hostname); err != nil {
			return err
		}
		cfg.TLS.CertFile = cert
		cfg.TLS.KeyFile = key
		cfg.IMAP.IMAPSAddr = "127.0.0.1:1993"
		cfg.SMTP.TLSAddr = "127.0.0.1:1465"
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	email := cmd.String("email")
	if email == "" {
		fmt.Printf("next: perch account add --config %s --email you@example.com\n", path)
		return nil
	}
	passwordValue := cmd.String("password")
	if passwordValue == "" {
		value, err := promptPassword("Password: ")
		if err != nil {
			return err
		}
		passwordValue = value
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	hash, err := password.Hash(passwordValue)
	if err != nil {
		return err
	}
	account, err := st.CreateAccount(ctx, email, cmd.String("name"), hash)
	if err != nil {
		return err
	}
	fmt.Printf("created account %s (id %d)\n", account.PrimaryAddress, account.ID)
	fmt.Printf("next: perch serve --config %s\n", path)
	return nil
}
