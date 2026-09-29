package cmd

import (
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
	var port int
	var watch bool
	cmd := &cobra.Command{
		Use:   "dev [-- cmd...]",
		Short: "Run handler locally with stable URL + auto-reload",
		Long:  "Run the handler locally with PORT + ACTIONS_SIDECAR_URL set, behind a stable dev gateway (http://localhost:PORT) that translates plain HTTP into POST /invoke. Extra args after -- replace the default handler command.",
		Example: `  oort dev
  oort dev --port 3000 --watch
  oort dev --port 0 --watch=false
  oort dev -- bun src/index.ts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			urlChanged := cmd.Flags().Changed("url")
			offlineChanged := cmd.Flags().Changed("offline")
			if url == "" {
				url = baseURL()
			}
			// Default to offline mock unless the user explicitly points
			// at a control plane via --url.
			offlineEff := offline
			if !offlineChanged && urlChanged {
				offlineEff = false
			}
			if !offlineChanged && !urlChanged {
				offlineEff = true
			}
			return cli.DevWithOptions(cmd.Context(), cli.DevOptions{
				Dir: dir, URL: url, Offline: offlineEff,
				CmdArgs: args, Port: port, Watch: watch,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "function dir")
	cmd.Flags().StringVar(&url, "url", "", "control plane base (proxy mode)")
	cmd.Flags().BoolVar(&offline, "offline", true, "use in-memory sidecar mock")
	cmd.Flags().IntVar(&port, "port", 3000, "stable dev gateway port (0 = random free port)")
	cmd.Flags().BoolVar(&watch, "watch", true, "restart handler on file change")
	return cmd
}
