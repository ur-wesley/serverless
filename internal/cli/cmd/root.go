// Package cmd implements the `oort` Cobra command tree. Flag names,
// env vars and plain-text fallback output stay compatible with the
// previous hand-rolled CLI; Lipgloss styling applies on TTYs only.
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ur-wesley/oort/internal/cli"
	"github.com/ur-wesley/oort/internal/cli/ui"
	"github.com/ur-wesley/oort/internal/version"
)

// Options carries the global flags.
type Options struct {
	URLFlag   string
	TokenFlag string
	Plain     bool
	Output    string
}

var opts Options

// NewRootCmd builds the `oort` command tree (exported for tests).
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "oort",
		Short: "Oort binary platform CLI",
		Long: `oort - Oort binary platform CLI.

Control plane selection (first match wins):
  --url flag, $ACTIONS_URL, saved login, http://localhost:8080
Auth (first match wins):
  --token flag, $ACTIONS_TOKEN, saved login`,
		Example: `  oort login
  oort init --runtime ts --name hello
  oort deploy
  oort invoke hello --data '{"hi":1}'
  oort logs -f hello`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			ui.Plain = opts.Plain || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
			if out := cmd.OutOrStdout(); out != nil {
				if f, ok := out.(*os.File); ok {
					if st, err := f.Stat(); err == nil && (st.Mode()&os.ModeCharDevice) == 0 {
						ui.Plain = true
					}
				}
			}
		},
	}
	root.Version = version.Version
	root.SetVersionTemplate("{{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringVar(&opts.URLFlag, "url", "", "control plane base (default http://localhost:8080, saved login, $ACTIONS_URL)")
	pf.StringVar(&opts.TokenFlag, "token", "", "operator token override ($ACTIONS_TOKEN, saved login)")
	pf.BoolVar(&opts.Plain, "plain", false, "disable colors and decoration (also NO_COLOR=1)")
	pf.StringVarP(&opts.Output, "output", "o", "text", "output format: text|json (where supported)")

	root.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newConfigCmd(),
		newInitCmd(),
		newDevCmd(),
		newDeployCmd(),
		newJobsCmd(),
		newInvokeCmd(),
		newLogsCmd(),
		newLsCmd(),
		newKeysCmd(),
		newVersionCmd(),
	)
	return root
}

// Execute runs the root command with default stdio.
func Execute() error {
	return NewRootCmd().Execute()
}

func baseURL() string { return cli.ResolveURL(opts.URLFlag) }
func token() string   { return cli.ResolveToken(opts.TokenFlag) }
func jsonOut() bool   { return opts.Output == "json" }

func printJSON(cmd *cobra.Command, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	return err
}

func failf(format string, a ...any) error {
	return fmt.Errorf("%s", ui.ErrLine(fmt.Sprintf(format, a...)))
}

func out(cmd *cobra.Command) io.Writer { return cmd.OutOrStdout() }
