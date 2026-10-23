// Package cli implements the perch command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/store"
	cli "github.com/urfave/cli/v3"
	"golang.org/x/term"
)

// Version is the user-visible build version. Release builds override it with
// -ldflags "-X github.com/synqronlabs/perch/internal/cli.Version=<version>".
var Version = "0.1.0-dev"

// Run executes a subcommand and returns a process exit code.
func Run(args []string) int {
	cli.VersionPrinter = func(cmd *cli.Command) {
		fmt.Printf("perch %s\n", cmd.Version)
	}
	cmd := newRootCommand()
	err := cmd.Run(context.Background(), append([]string{"perch"}, args...))
	if err == nil {
		return 0
	}
	var exit cli.ExitCoder
	if errors.As(err, &exit) {
		if message := err.Error(); message != "" {
			fmt.Fprintln(os.Stderr, "perch:", message)
		}
		return exit.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "perch:", err)
	return 1
}

func newRootCommand() *cli.Command {
	return &cli.Command{
		Name:      "perch",
		Usage:     "a small IMAP mailbox service backed by mxRaven",
		Version:   Version,
		Writer:    os.Stderr,
		ErrWriter: os.Stderr,
		ExitErrHandler: func(context.Context, *cli.Command, error) {
			// Run maps errors to process exit codes itself so urfave/cli does
			// not call os.Exit inside tests or embedders.
		},
		CommandNotFound: func(_ context.Context, cmd *cli.Command, name string) {
			_, _ = fmt.Fprintf(cmd.Root().ErrWriter, "unknown command %q\n\n", name)
			_ = cli.ShowRootCommandHelp(cmd)
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() > 0 {
				_, _ = fmt.Fprintf(cmd.Root().ErrWriter, "unknown command %q\n\n", cmd.Args().First())
			}
			_ = cli.ShowRootCommandHelp(cmd)
			return cli.Exit("", 2)
		},
		Commands: []*cli.Command{
			initCommand(),
			setupCommand(),
			serveCommand(),
			accountCommand(),
			aliasCommand(),
			mailboxCommand(),
			doctorCommand(),
			backupCommand(),
			reconcileCommand(),
			gcCommand(),
			outboundCommand(),
			versionCommand(),
		},
	}
}

func versionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "print the version",
		Action: func(context.Context, *cli.Command) error {
			fmt.Printf("perch %s\n", Version)
			return nil
		},
	}
}

func configFlag(defaultValue string) cli.Flag {
	return &cli.StringFlag{Name: "config", Value: defaultValue, Usage: "configuration file"}
}

func commandUsage(usage string) cli.ActionFunc {
	return func(context.Context, *cli.Command) error {
		return cli.Exit(usage, 1)
	}
}

func missingOrUnknown(usage, noun string) cli.ActionFunc {
	return func(_ context.Context, cmd *cli.Command) error {
		if cmd.Args().Len() > 0 {
			return fmt.Errorf("unknown %s command %q", noun, cmd.Args().First())
		}
		return cli.Exit(usage, 1)
	}
}

func openConfigStore(path string) (config.Config, *store.Store, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, st, nil
}

func withConfigStore(path string, fn func(config.Config, *store.Store) error) error {
	cfg, st, err := openConfigStore(path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return fn(cfg, st)
}

func defaultConfigPath() string {
	if path := os.Getenv("PERCH_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(config.Default().DataDir, "perch.yaml")
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.Format, "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func promptPassword(label string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, label)
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
	reader := bufio.NewReader(os.Stdin)
	value, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(value, "\r\n"), nil
}

func printTable(headers []string, rows [][]string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	return w.Flush()
}
