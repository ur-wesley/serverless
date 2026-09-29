package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ur-wesley/serverless/internal/cli"
	"github.com/ur-wesley/serverless/internal/cli/ui"
	"github.com/ur-wesley/serverless/internal/deploy"
)

func newDeployCmd() *cobra.Command {
	var dir, url string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Zip src, upload, wait until active",
		Example: `  oort deploy
  oort deploy --dir ./hello --no-wait`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				url = baseURL()
			}
			tok := token()
			tomlRaw, err := os.ReadFile(filepath.Join(dir, "actions.toml"))
			if err != nil {
				return err
			}
			cfg, err := deploy.ParseConfig(string(tomlRaw))
			if err != nil {
				return err
			}
			sp := ui.NewSpinner(cmd.ErrOrStderr(), "")
			sp.Message("packing source")
			sp.Start()
			zipBytes, err := cli.ZipDir(dir)
			if err != nil {
				sp.Stop()
				return err
			}
			sp.Message("uploading")
			job, err := cli.EnqueueDeploy(url, tok, cfg.Name, string(tomlRaw), zipBytes)
			if err != nil {
				sp.Stop()
				return err
			}
			sp.Stop()
			fmt.Fprintf(out(cmd), "%s job %s queued (%s %s)\n",
				ui.Success("ok"), ui.Code(job.ID), job.FnName, ui.Code(job.Version))
			if noWait {
				return nil
			}
			sp2 := ui.NewSpinner(cmd.ErrOrStderr(), "")
			sp2.Message("building " + job.ID)
			sp2.Start()
			defer sp2.Stop()
			shown := 0
			quiet := ui.Plain
			deadline := time.Now().Add(10 * time.Minute)
			for {
				if time.Now().After(deadline) {
					return failf("timed out waiting for job %s (oort jobs %s)", job.ID, job.FnName)
				}
				time.Sleep(2 * time.Second)
				j, err := cli.GetDeployJob(url, tok, job.ID)
				if err != nil {
					return err
				}
				if logs, err := cli.GetDeployLogs(url, tok, job.ID); err == nil && len(logs) > shown {
					if quiet {
						fmt.Fprint(out(cmd), logs[shown:])
					} else {
						sp2.Stop()
						fmt.Fprint(out(cmd), logs[shown:])
						sp2.Start()
					}
					shown = len(logs)
				}
				sp2.Message(fmt.Sprintf("%s %s", j.Status, shortID(j.ID)))
				switch j.Status {
				case "active":
					sp2.Stop()
					fmt.Fprintf(out(cmd), "%s %s %s image=%s sha256=%.12s\n",
						ui.Code(j.FnName), ui.Code(j.Version), ui.Status(j.Status), j.Image, j.SHA256)
					return nil
				case "failed":
					return failf("deploy %s failed: %s", j.ID, j.Error)
				}
			}
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "function dir")
	cmd.Flags().StringVar(&url, "url", "", "control plane base")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "enqueue and exit without waiting")
	return cmd
}

func newJobsCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "jobs NAME",
		Short: "List deploy jobs for a function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = baseURL()
			}
			jobs, err := cli.ListDeployJobs(url, token(), args[0])
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				fmt.Fprintln(out(cmd), ui.Dim("(no jobs)"))
				return nil
			}
			if jsonOut() {
				return printJSON(cmd, jobs)
			}
			rows := make([][]string, 0, len(jobs))
			for _, j := range jobs {
				errCell := j.Error
				if errCell != "" {
					errCell = ui.ErrLine(errCell)
				}
				rows = append(rows, []string{shortID(j.ID), j.Version, ui.Status(j.Status), j.CreatedAt, errCell})
			}
			fmt.Fprint(out(cmd), ui.Table([]string{"ID", "VERSION", "STATUS", "CREATED", "ERROR"}, rows, 2))
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "control plane base")
	return cmd
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
