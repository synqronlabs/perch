package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/store"
	cli "github.com/urfave/cli/v3"
)

func aliasCommand() *cli.Command {
	return &cli.Command{
		Name:   "alias",
		Usage:  "manage address aliases and catch-all domains",
		Action: missingOrUnknown("usage: perch alias add|remove|list", "alias"),
		Commands: []*cli.Command{
			{
				Name:  "add",
				Usage: "add an alias or catch-all",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "account", Usage: "account email"},
					&cli.StringFlag{Name: "address", Usage: "alias address or domain"},
					&cli.BoolFlag{Name: "catch-all", Usage: "route all unmatched addresses at --address"},
				},
				Action: runAliasAdd,
			},
			{
				Name:  "remove",
				Usage: "remove an alias",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "address", Usage: "alias address"},
				},
				Action: runAliasRemove,
			},
			{
				Name:  "list",
				Usage: "list aliases for an account",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "account", Usage: "account email"},
				},
				Action: runAliasList,
			},
		},
	}
}

func runAliasAdd(ctx context.Context, cmd *cli.Command) error {
	accountEmail := cmd.String("account")
	address := cmd.String("address")
	if accountEmail == "" || address == "" {
		return errors.New("--account and --address are required")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		account, err := st.AccountByAddress(ctx, accountEmail)
		if err != nil {
			return err
		}
		if cmd.Bool("catch-all") {
			err = st.AddCatchAll(ctx, account.ID, address)
		} else {
			err = st.AddAlias(ctx, account.ID, address)
		}
		if err != nil {
			return err
		}
		fmt.Printf("added %s to %s\n", address, account.PrimaryAddress)
		return nil
	})
}

func runAliasRemove(ctx context.Context, cmd *cli.Command) error {
	address := cmd.String("address")
	if address == "" {
		return errors.New("--address is required")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		if err := st.RemoveAddress(ctx, address); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", address)
		return nil
	})
}

func runAliasList(ctx context.Context, cmd *cli.Command) error {
	accountEmail := cmd.String("account")
	if accountEmail == "" {
		return errors.New("--account is required")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		account, err := st.AccountByAddress(ctx, accountEmail)
		if err != nil {
			return err
		}
		addresses, err := st.ListAddresses(ctx, account.ID)
		if err != nil {
			return err
		}
		for _, address := range addresses {
			fmt.Println(address)
		}
		return nil
	})
}
