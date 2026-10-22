package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/store"
	cli "github.com/urfave/cli/v3"
)

func mailboxCommand() *cli.Command {
	return &cli.Command{
		Name:   "mailbox",
		Usage:  "list mailboxes for an account",
		Action: commandUsage("usage: perch mailbox list --account you@example.com"),
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "list mailboxes for an account",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "account", Usage: "account email"},
				},
				Action: runMailboxList,
			},
		},
	}
}

func runMailboxList(ctx context.Context, cmd *cli.Command) error {
	accountEmail := cmd.String("account")
	if accountEmail == "" {
		return errors.New("--account is required")
	}
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		account, err := st.AccountByAddress(ctx, accountEmail)
		if err != nil {
			return err
		}
		mailboxes, err := st.ListMailboxes(ctx, account.ID)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(mailboxes))
		for _, mailbox := range mailboxes {
			rows = append(rows, []string{mailbox.Name, mailbox.SpecialUse, fmt.Sprint(mailbox.Subscribed)})
		}
		return printTable([]string{"NAME", "SPECIAL-USE", "SUBSCRIBED"}, rows)
	})
}
