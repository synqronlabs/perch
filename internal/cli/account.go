package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
	cli "github.com/urfave/cli/v3"
)

func accountCommand() *cli.Command {
	return &cli.Command{
		Name:   "account",
		Usage:  "add, list, password, disable, or enable accounts",
		Action: missingOrUnknown("usage: perch account add|list|passwd|disable|enable", "account"),
		Commands: []*cli.Command{
			{
				Name:  "add",
				Usage: "create an account",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "email", Usage: "account email"},
					&cli.StringFlag{Name: "name", Usage: "display name"},
					&cli.StringFlag{Name: "password", Usage: "password (prompted when empty)"},
				},
				Action: runAccountAdd,
			},
			{
				Name:   "list",
				Usage:  "list accounts",
				Flags:  []cli.Flag{configFlag(defaultConfigPath())},
				Action: runAccountList,
			},
			{
				Name:  "passwd",
				Usage: "change an account password",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "email", Usage: "account email"},
					&cli.StringFlag{Name: "password", Usage: "new password (prompted when empty)"},
				},
				Action: runAccountPasswd,
			},
			accountToggleCommand("disable", true),
			accountToggleCommand("enable", false),
		},
	}
}

func runAccountAdd(ctx context.Context, cmd *cli.Command) error {
	email := cmd.String("email")
	if email == "" {
		return errors.New("--email is required")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		passwordValue := cmd.String("password")
		if passwordValue == "" {
			value, err := promptPassword("Password: ")
			if err != nil {
				return err
			}
			passwordValue = value
		}
		hash, err := password.Hash(passwordValue)
		if err != nil {
			return err
		}
		account, err := st.CreateAccount(ctx, email, cmd.String("name"), hash)
		if err != nil {
			return err
		}
		fmt.Printf("created %s (id %d)\n", account.PrimaryAddress, account.ID)
		return nil
	})
}

func runAccountList(ctx context.Context, cmd *cli.Command) error {
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		accounts, err := st.ListAccounts(ctx)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(accounts))
		for _, account := range accounts {
			rows = append(rows, []string{
				fmt.Sprint(account.ID), account.PrimaryAddress, account.DisplayName,
				fmt.Sprint(account.Disabled), account.CreatedAt.Format(time.RFC3339),
			})
		}
		return printTable([]string{"ID", "ADDRESS", "NAME", "DISABLED", "CREATED"}, rows)
	})
}

func runAccountPasswd(ctx context.Context, cmd *cli.Command) error {
	email := cmd.String("email")
	if email == "" {
		return errors.New("--email is required")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		account, err := st.AccountByAddress(ctx, email)
		if err != nil {
			return err
		}
		passwordValue := cmd.String("password")
		if passwordValue == "" {
			value, err := promptPassword("New password: ")
			if err != nil {
				return err
			}
			passwordValue = value
		}
		hash, err := password.Hash(passwordValue)
		if err != nil {
			return err
		}
		if err := st.SetPasswordHash(ctx, account.ID, hash); err != nil {
			return err
		}
		fmt.Printf("updated password for %s\n", account.PrimaryAddress)
		return nil
	})
}

func accountToggleCommand(name string, disabled bool) *cli.Command {
	return &cli.Command{
		Name:  name,
		Usage: name + " an account",
		Flags: []cli.Flag{
			configFlag(defaultConfigPath()),
			&cli.StringFlag{Name: "email", Usage: "account email"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			email := cmd.String("email")
			if email == "" {
				return errors.New("--email is required")
			}
			return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
				account, err := st.AccountByAddress(ctx, email)
				if err != nil {
					return err
				}
				if err := st.SetAccountDisabled(ctx, account.ID, disabled); err != nil {
					return err
				}
				fmt.Printf("%s %s\n", name+"d", account.PrimaryAddress)
				return nil
			})
		},
	}
}
