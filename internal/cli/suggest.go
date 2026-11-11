package cli

import (
	"context"
	"fmt"
	"strings"

	cli "github.com/urfave/cli/v3"
)

// suggestSubcommand returns the closest command or alias when the provided
// argument is a likely typo. Small edit distances only: a suggestion should
// help with a misspelling, not guess arbitrary input.
func suggestSubcommand(commands []*cli.Command, provided string) string {
	provided = strings.ToLower(strings.TrimSpace(provided))
	if provided == "" || strings.HasPrefix(provided, "-") {
		return ""
	}

	best := ""
	bestDistance := -1
	for _, command := range commands {
		for _, name := range command.Names() {
			distance := editDistance(provided, strings.ToLower(name))
			if bestDistance < 0 || distance < bestDistance ||
				(distance == bestDistance && name == command.Name) {
				best = command.Name
				bestDistance = distance
			}
		}
	}
	if best == "" {
		return ""
	}

	limit := 2
	if len([]rune(provided)) <= 3 {
		limit = 1
	}
	if bestDistance > limit {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	left := []rune(a)
	right := []rune(b)
	previous := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current := make([]int, len(right)+1)
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 0
			if left[i-1] != right[j-1] {
				cost = 1
			}
			current[j] = min(
				current[j-1]+1,
				previous[j]+1,
				previous[j-1]+cost,
			)
		}
		previous = current
	}
	return previous[len(right)]
}

// commandUsageError keeps the default usage output but adds a subcommand
// suggestion when a typo would otherwise be reported only as an unknown flag.
func commandUsageError(_ context.Context, cmd *cli.Command, err error, _ bool) error {
	_, _ = fmt.Fprintf(cmd.Root().ErrWriter, "Incorrect Usage: %s\n\n", err.Error())
	if suggestion := suggestSubcommand(cmd.Commands, cmd.Args().First()); suggestion != "" {
		_, _ = fmt.Fprintf(cmd.Root().ErrWriter, "Did you mean %q?\n\n", suggestion)
		if cmd.Root() == cmd {
			_ = cli.ShowRootCommandHelp(cmd)
		} else {
			_ = cli.ShowSubcommandHelp(cmd)
		}
		return fmt.Errorf("unknown command %q; did you mean %q?", cmd.Args().First(), suggestion)
	}
	if cmd.Root() == cmd {
		_ = cli.ShowRootCommandHelp(cmd)
	} else {
		_ = cli.ShowSubcommandHelp(cmd)
	}
	return err
}

func unknownCommandError(cmd *cli.Command, name string) error {
	if suggestion := suggestSubcommand(cmd.Commands, name); suggestion != "" {
		return fmt.Errorf("unknown command %q; did you mean %q?", name, suggestion)
	}
	return fmt.Errorf("unknown command %q", name)
}
