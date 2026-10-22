package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/store"
	cli "github.com/urfave/cli/v3"
)

func outboundCommand() *cli.Command {
	return &cli.Command{
		Name:   "outbound",
		Usage:  "list submitted messages and delivery status",
		Action: commandUsage("usage: perch outbound list [--account you@example.com]"),
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "list submitted messages",
				Flags: []cli.Flag{
					configFlag(defaultConfigPath()),
					&cli.StringFlag{Name: "account", Usage: "filter by account email"},
					&cli.IntFlag{Name: "limit", Value: 100, Usage: "maximum rows"},
				},
				Action: runOutboundList,
			},
		},
	}
}

func runOutboundList(ctx context.Context, cmd *cli.Command) error {
	return withConfigStore(cmd.String("config"), func(_ config.Config, st *store.Store) error {
		var accountID int64
		if accountEmail := cmd.String("account"); accountEmail != "" {
			account, err := st.AccountByAddress(ctx, accountEmail)
			if err != nil {
				return err
			}
			accountID = account.ID
		}
		rows, err := st.ListOutbound(ctx, accountID, cmd.Int("limit"))
		if err != nil {
			return err
		}
		table := make([][]string, 0, len(rows))
		for _, row := range rows {
			smtp := ""
			if row.SMTPCode != 0 {
				smtp = fmt.Sprintf("%d %s", row.SMTPCode, row.EnhancedStatus)
			}
			table = append(table, []string{
				row.CreatedAt.Format(time.RFC3339), row.Status, row.FromAddress, row.Subject,
				row.MessageRef, strconv.FormatUint(row.Attempt, 10), smtp,
			})
		}
		return printTable([]string{"CREATED", "STATUS", "FROM", "SUBJECT", "MESSAGE_REF", "ATTEMPT", "SMTP"}, table)
	})
}
