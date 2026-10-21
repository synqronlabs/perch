// Command perch runs a small IMAP mailbox service backed by mxRaven.
package main

import (
	"os"

	"github.com/synqronlabs/perch/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
