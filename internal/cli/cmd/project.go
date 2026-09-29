package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/ur-wesley/oort/internal/cli"
	"github.com/ur-wesley/oort/internal/cli/ui"
)

func newInitCmd() *cobra.Command {
	var runtime, name, dir string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold actions.toml + src",
		Example: `  oort init --runtime ts --name hello
  oort init --runtime go --name hello --dir ./hello`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if isTTY() && (runtime == "" || name == "") {
				rt := runtime
				nm := name
				form := huh.NewForm(huh.NewGroup(
					huh.NewSelect[string]().Title("Runtime").Options(
						huh.NewOption("TypeScript (bun)", "ts"),
						huh.NewOption("Go", "go"),
					).Value(&rt),
					huh.NewInput().Title("Function name").Value(&nm).Placeholder("hello"),
				))
				if err := form.Run(); err != nil {
					return err
				}
				runtime, name = rt, strings.TrimSpace(nm)
			}
			if dir == "" {
				dir = "./" + name
			}
			if runtime == "" || name == "" {
				return failf("usage: oort init --runtime ts|go --name NAME [--dir DIR]")
			}
			if err := cli.Init(runtime, name, dir); err != nil {
				return err
			}
			fmt.Fprintf(out(cmd), "%s scaffolded %s (%s) in %s\n",
				ui.Success("ok"), ui.Code(name), runtime, ui.Code(dir))
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "ts|go")
	cmd.Flags().StringVar(&name, "name", "", "function name")
	cmd.Flags().StringVar(&dir, "dir", "", "target dir (default ./NAME)")
	return cmd
}

func newDevCmd() *cobra.Command {
	var dir, url string
	var offline bool
	cmd := &cobra.Command{
		Use:   "dev [-- cmd...]",
		Short: "Run handler locally",
		Long:  "Run the handler locally with PORT + ACTIONS_SIDECAR_URL set. Extra args after -- replace the default handler command.",
		Example: `  oort dev
  oort dev --offline
  oort dev -- bun src/index.ts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = baseURL()
			}
			return cli.Dev(context.Background(), dir, url, offline, args)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "function dir")
	cmd.Flags().StringVar(&url, "url", "", "control plane base (proxy mode)")
	cmd.Flags().BoolVar(&offline, "offline", false, "use in-memory sidecar mock")
	return cmd
}
