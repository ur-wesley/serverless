package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ur-wesley/oort/internal/cli"
	"github.com/ur-wesley/oort/internal/cli/ui"
	"github.com/ur-wesley/oort/internal/version"
)

func newKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage per-function API keys (multiple per app)",
	}
	cmd.AddCommand(newKeysCreateCmd(), newKeysLsCmd(), newKeysRevokeCmd())
	return cmd
}

func newKeysCreateCmd() *cobra.Command {
	var name, url string
	cmd := &cobra.Command{
		Use:   "create APP",
		Short: "Create an API key for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = baseURL()
			}
			created, err := cli.CreateAPIKey(url, token(), args[0], name)
			if err != nil {
				return err
			}
			if jsonOut() {
				return printJSON(cmd, created)
			}
			fmt.Fprintf(out(cmd), "id=%s prefix=%s\nkey=%s\n%s\n",
				ui.Code(created.ID), created.Prefix, ui.Code(created.Key),
				ui.Warn("(shown once — store it now)"))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "key label")
	cmd.Flags().StringVar(&url, "url", "", "control plane base")
	return cmd
}

func newKeysLsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "ls APP",
		Short:   "List API keys for an app",
		Aliases: []string{"list"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := cli.ListAPIKeys(baseURL(), token(), args[0])
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Fprintln(out(cmd), ui.Dim("(no keys)"))
				return nil
			}
			if jsonOut() {
				return printJSON(cmd, keys)
			}
			rows := make([][]string, 0, len(keys))
			for _, k := range keys {
				rev := k.RevokedAt
				if rev != "" {
					rev = ui.ErrLine(rev)
				} else {
					rev = "-"
				}
				rows = append(rows, []string{k.ID, k.Name, k.Prefix, k.CreatedAt, k.LastUsedAt, rev})
			}
			fmt.Fprint(out(cmd), ui.Table(
				[]string{"ID", "NAME", "PREFIX", "CREATED", "LAST USED", "REVOKED"}, rows))
			return nil
		},
	}
	return c
}

func newKeysRevokeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "revoke KEYID",
		Short:   "Revoke an API key",
		Aliases: []string{"rm", "delete"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.RevokeAPIKey(baseURL(), token(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(out(cmd), "%s revoked %s\n", ui.Success("ok"), ui.Code(args[0]))
			return nil
		},
	}
	return c
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print platform version (from package.json)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(out(cmd), version.Version)
			return nil
		},
	}
}
