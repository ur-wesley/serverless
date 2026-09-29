package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ur-wesley/oort/internal/cli"
	"github.com/ur-wesley/oort/internal/cli/ui"
)

func newInvokeCmd() *cobra.Command {
	var method, data, url, apiKey string
	cmd := &cobra.Command{
		Use:   "invoke NAME[/path]",
		Short: "Call a function",
		Long:  "Call a function. NAME may include a namespace prefix (NAMESPACE/NAME[/path]).",
		Example: `  oort invoke hello
  oort invoke hello/echo --data '{"hi":1}'
  oort invoke -d 'raw body' hello`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = baseURL()
			}
			if apiKey == "" {
				apiKey = os.Getenv("ACTIONS_API_KEY")
			}
			m := method
			if m == "" {
				if data != "" {
					m = "POST"
				} else {
					m = "GET"
				}
			}
			res, err := cli.InvokeWithAuth(url, token(), apiKey, args[0], m, []byte(data))
			if err != nil {
				return err
			}
			if jsonOut() {
				return printJSON(cmd, map[string]any{
					"status":  res.Status,
					"headers": res.Headers,
					"body":    string(res.Body),
				})
			}
			body := res.Body
			if isJSON(body) {
				var v any
				if err := json.Unmarshal(body, &v); err == nil {
					if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
						body = pretty
					}
				}
			}
			fmt.Fprintf(out(cmd), "status: %s\n%s\n", ui.HTTPStatus(res.Status), body)
			if res.Status >= 400 {
				return failf("invoke: status %d", res.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&method, "method", "", "HTTP method (default POST with -d, else GET)")
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body")
	cmd.Flags().StringVar(&url, "url", "", "control plane base")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key for key-protected functions ($ACTIONS_API_KEY)")
	return cmd
}

func newLogsCmd() *cobra.Command {
	var follow bool
	var url string
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show/follow function logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = baseURL()
			}
			name := args[0]
			seen := map[string]bool{}
			show := func() error {
				lines, err := cli.GetLogsWithAuth(url, token(), name)
				if err != nil {
					return err
				}
				if jsonOut() {
					return printJSON(cmd, lines)
				}
				for _, l := range lines {
					id := l.Time.Format(time.RFC3339Nano) + l.RequestID + l.Line
					if seen[id] {
						continue
					}
					seen[id] = true
					if len(seen) > 5000 {
						seen = map[string]bool{id: true}
					}
					fmt.Fprintf(out(cmd), "%s [%s/%s] %s\n",
						ui.Dim(l.Time.Format(time.RFC3339)), l.Version, ui.Code(l.RequestID), l.Line)
				}
				return nil
			}
			if err := show(); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			for {
				time.Sleep(2 * time.Second)
				if err := show(); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow")
	cmd.Flags().StringVar(&url, "url", "", "control plane base")
	return cmd
}

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Short:   "List your functions",
		Aliases: []string{"list"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			fns, err := cli.ListFunctionsWithAuth(baseURL(), token())
			if err != nil {
				return err
			}
			if len(fns) == 0 {
				fmt.Fprintln(out(cmd), ui.Dim("(no functions)"))
				return nil
			}
			if jsonOut() {
				return printJSON(cmd, fns)
			}
			if ui.Plain {
				fmt.Fprint(out(cmd), cli.FormatFunctions(fns, time.Now()))
				return nil
			}
			now := time.Now()
			rows := make([][]string, 0, len(fns))
			for _, f := range fns {
				rows = append(rows, cli.FunctionRow(f, now))
			}
			fmt.Fprint(out(cmd), ui.Table(cli.FunctionHeader(), rows, 4))
			return nil
		},
	}
}

func isJSON(b []byte) bool {
	var v any
	return json.Unmarshal(b, &v) == nil
}
