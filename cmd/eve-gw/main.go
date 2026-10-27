// Command eve-gw is the host-side admin CLI for the Eve gateway (pairing and
// browser sessions). See internal/cli.
package main

import (
	"fmt"
	"os"

	"github.com/gun025bba/eve-serving/internal/cli"
)

func main() {
	if !cli.IsCommand(os.Args[1:]) {
		fmt.Fprintln(os.Stderr, "usage: eve-gw pair --user <login> | eve-gw sessions list | eve-gw sessions revoke <id>")
		os.Exit(2)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
