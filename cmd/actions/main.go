// Command actions is the developer CLI: init/dev/deploy/invoke/logs/ls.
// Usage: go run ./cmd/actions --help
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actions/internal/cli"
	"actions/internal/deploy"
	"actions/internal/version"
)

const usage = `actions - serverless binary platform CLI

Usage:
  actions [--url URL] <command> [flags]

Global flags:
  --url URL   control plane base (default http://localhost:8080)

Commands:
  init    --runtime ts|go --name NAME [--dir DIR]   scaffold actions.toml + src
  dev     [--dir DIR] [--url URL] [--offline] [-- cmd...]  run handler locally
  deploy  [--dir DIR]                                zip src, upload, activate
  invoke  [--method M] [--data BODY] [-d BODY] NAME[/path]  call a function
  logs    [-f] NAME                                  show/follow function logs
  ls                                               list functions
  version                                          print platform version (from package.json)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	baseURL := "http://localhost:8080"
	rest := args
	for len(rest) >= 2 && rest[0] == "--url" {
		baseURL = rest[1]
		rest = rest[2:]
	}
	if len(rest) == 0 || rest[0] == "--help" || rest[0] == "-h" {
		fmt.Print(usage)
		return nil
	}
	if rest[0] == "--version" || rest[0] == "-v" {
		fmt.Println(version.Version)
		return nil
	}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "version":
		fmt.Println(version.Version)
		return nil
	case "init":
		return cmdInit(cmdArgs)
	case "dev":
		return cmdDev(cmdArgs, baseURL)
	case "deploy":
		return cmdDeploy(cmdArgs, baseURL)
	case "invoke":
		return cmdInvoke(cmdArgs, baseURL)
	case "logs":
		return cmdLogs(cmdArgs, baseURL)
	case "ls":
		return cmdLs(baseURL)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	runtime := fs.String("runtime", "", "ts|go")
	name := fs.String("name", "", "function name")
	dir := fs.String("dir", "", "target dir (default ./NAME)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		*dir = "./" + *name
	}
	if err := cli.Init(*runtime, *name, *dir); err != nil {
		return err
	}
	fmt.Printf("scaffolded %s (%s) in %s\n", *name, *runtime, *dir)
	return nil
}

func cmdDev(args []string, baseURL string) error {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	dir := fs.String("dir", ".", "function dir")
	url := fs.String("url", baseURL, "control plane base (proxy mode)")
	offline := fs.Bool("offline", false, "use in-memory sidecar mock")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return cli.Dev(context.Background(), *dir, *url, *offline, fs.Args())
}

func cmdDeploy(args []string, baseURL string) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	dir := fs.String("dir", ".", "function dir")
	url := fs.String("url", baseURL, "control plane base")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tomlRaw, err := os.ReadFile(filepath.Join(*dir, "actions.toml"))
	if err != nil {
		return err
	}
	cfg, err := deploy.ParseConfig(string(tomlRaw))
	if err != nil {
		return err
	}
	zipBytes, err := cli.ZipDir(*dir)
	if err != nil {
		return err
	}
	res, err := cli.Deploy(*url, cfg.Name, string(tomlRaw), zipBytes)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s image=%s sha256=%.12s\n", res.Name, res.Version, res.Status, res.Image, res.SHA256)
	return nil
}

func cmdInvoke(args []string, baseURL string) error {
	fs := flag.NewFlagSet("invoke", flag.ContinueOnError)
	method := fs.String("method", "", "HTTP method (default POST with -d, else GET)")
	data := fs.String("data", "", "request body")
	fs.StringVar(data, "d", "", "request body (shorthand)")
	url := fs.String("url", baseURL, "control plane base")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: actions invoke NAME[/path]")
	}
	m := *method
	if m == "" {
		if *data != "" {
			m = "POST"
		} else {
			m = "GET"
		}
	}
	res, err := cli.Invoke(*url, fs.Arg(0), m, []byte(*data))
	if err != nil {
		return err
	}
	fmt.Printf("status: %d\n%s\n", res.Status, res.Body)
	return nil
}

func cmdLogs(args []string, baseURL string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow")
	url := fs.String("url", baseURL, "control plane base")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: actions logs [-f] NAME")
	}
	name := fs.Arg(0)
	seen := map[string]bool{}
	show := func() error {
		lines, err := cli.GetLogs(*url, name)
		if err != nil {
			return err
		}
		for _, l := range lines {
			id := l.Time.Format(time.RFC3339Nano) + l.RequestID + l.Line
			if seen[id] {
				continue
			}
			seen[id] = true
			fmt.Printf("%s [%s/%s] %s\n", l.Time.Format(time.RFC3339), l.Version, l.RequestID, l.Line)
		}
		return nil
	}
	if err := show(); err != nil {
		return err
	}
	if !*follow {
		return nil
	}
	for {
		time.Sleep(2 * time.Second)
		if err := show(); err != nil {
			return err
		}
	}
}

func cmdLs(baseURL string) error {
	fns, err := cli.ListFunctions(baseURL)
	if err != nil {
		return err
	}
	if len(fns) == 0 {
		fmt.Println("(no functions)")
		return nil
	}
	w := 8
	for _, f := range fns {
		if len(f.Name) > w {
			w = len(f.Name)
		}
	}
	for _, f := range fns {
		fmt.Printf("%-*s %s\n", w, strings.TrimSpace(f.Name), f.ActiveVersion)
	}
	return nil
}
