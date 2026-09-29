package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ur-wesley/oort/internal/cli"
	"github.com/ur-wesley/oort/internal/cli/ui"
)

func newLoginCmd() *cobra.Command {
	var urlFlag string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Device-code login (first login creates your account)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			url := cli.ResolveURL(urlFlag)
			if isTTY() && urlFlag == "" && os.Getenv("ACTIONS_URL") == "" {
				var input string
				form := huh.NewForm(huh.NewGroup(
					huh.NewInput().Title("Control plane URL").Value(&input).Placeholder(url),
				))
				if err := form.Run(); err != nil {
					return err
				}
				if strings.TrimSpace(input) != "" {
					url = cli.NormalizeURL(input)
				}
			} else {
				fmt.Fprintf(out(cmd), "control plane URL [%s]: ", ui.Code(url))
				if line, err := cli.Prompt(""); err == nil && strings.TrimSpace(line) != "" {
					url = cli.NormalizeURL(line)
				}
			}
			fmt.Fprintf(out(cmd), "contacting %s ...\n", ui.Code(url))
			host, _ := os.Hostname()
			start, err := cli.DeviceStartReq(url, "cli@"+host)
			if err != nil {
				return fmt.Errorf("login: %v (is the control plane reachable at %s?)", err, url)
			}
			panel := fmt.Sprintf("Verify this device:\n  %s%s\nCode: %s (expires in %d min)",
				url, start.VerifyPath, start.UserCode, start.ExpiresIn/60)
			fmt.Fprintln(out(cmd), ui.Panel(panel))
			fmt.Fprintln(out(cmd), ui.Dim("Waiting for browser verification..."))

			sp := ui.NewSpinner(cmd.ErrOrStderr(), "")
			sp.Message("waiting for verification")
			sp.Start()
			defer sp.Stop()
			deadline := time.Now().Add(time.Duration(start.ExpiresIn+30) * time.Second)
			for {
				if time.Now().After(deadline) {
					return failf("login expired — run `oort login` again")
				}
				time.Sleep(5 * time.Second)
				poll, err := cli.DevicePollReq(url, start.DeviceCode)
				if err != nil {
					return err
				}
				switch poll.Status {
				case "approved":
					if err := cli.SaveAuth(cli.Auth{URL: url, Token: poll.Token, Username: poll.Username}); err != nil {
						return err
					}
					sp.Stop()
					fmt.Fprintf(out(cmd), "%s Logged in as %s.\n", ui.Success("ok"), ui.Code(poll.Username))
					return nil
				case "denied":
					return failf("login denied")
				case "expired":
					return failf("login expired — run `oort login` again")
				}
			}
		},
	}
	cmd.Flags().StringVar(&urlFlag, "url", "", "control plane base")
	return cmd
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget saved login",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli.LogoutRemote(baseURL(), token())
			if err := cli.ClearAuth(); err != nil {
				return err
			}
			fmt.Fprintln(out(cmd), "logged out")
			return nil
		},
	}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show logged-in user",
		RunE: func(cmd *cobra.Command, _ []string) error {
			me, err := cli.Me(baseURL(), token())
			if err != nil {
				return err
			}
			if me.Setup {
				fmt.Fprintln(out(cmd), ui.Dim("no users yet — run `oort login` to create the first account"))
				return nil
			}
			if jsonOut() {
				return printJSON(cmd, me)
			}
			fmt.Fprintln(out(cmd), me.Name)
			return nil
		},
	}
}

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config [get url|set url VALUE]",
		Short: "Show or change saved control plane URL",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				a, _ := cli.LoadAuth()
				u := a.URL
				if u == "" {
					u = cli.ResolveURL("")
				}
				fmt.Fprintf(out(cmd), "url=%s\n", ui.Code(u))
				if a.Username != "" {
					fmt.Fprintf(out(cmd), "user=%s\n", a.Username)
				}
				return nil
			}
			if len(args) == 2 && args[0] == "get" && args[1] == "url" {
				fmt.Fprintln(out(cmd), cli.ResolveURL(""))
				return nil
			}
			if len(args) == 3 && args[0] == "set" && args[1] == "url" {
				url := cli.NormalizeURL(args[2])
				a, _ := cli.LoadAuth()
				a.URL = url
				if err := cli.SaveAuth(a); err != nil {
					return err
				}
				fmt.Fprintf(out(cmd), "%s url=%s\n", ui.Success("ok"), ui.Code(url))
				return nil
			}
			return failf("usage: oort config [get url|set url VALUE]")
		},
	}
}

func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && !ui.Plain
}
