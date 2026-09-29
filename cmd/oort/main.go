// Command oort is the developer CLI: init/dev/deploy/invoke/logs/ls
// plus login/logout/whoami and per-function API key management.
// Usage: oort --help
package main

import (
	"fmt"
	"os"

	cli "github.com/ur-wesley/oort/internal/cli/cmd"
	"github.com/ur-wesley/oort/internal/cli/ui"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.ErrLine("error:")+" "+err.Error())
		os.Exit(1)
	}
}
