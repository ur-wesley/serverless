// Command actions is the developer CLI: init/dev/deploy/invoke/logs/ls
// plus login/logout/whoami and per-function API key management.
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
  actions [--url URL] [--token TOKEN] <command> [flags]

Global flags:
  --url URL       control plane base (default http://localhost:8080, saved login, $ACTIONS_URL)
  --token TOKEN   operator token override ($ACTIONS_TOKEN, saved login)

Commands:
  login   [--url URL]                          device-code login (first login creates your account)
  logout                                       forget saved login
  whoami                                       show logged-in user
  config  [get|set url VALUE]                  show or change saved control plane URL
  init    --runtime ts|go --name NAME [--dir DIR]   scaffold actions.toml + src
  dev     [--dir DIR] [--url URL] [--offline] [-- cmd...]  run handler locally
  deploy  [--dir DIR]                          zip src, upload, activate
  invoke  [--method M] [--data BODY] [-d BODY] [--api-key KEY] NAME[/path]  call a function
  logs    [-f] NAME                            show/follow function logs
  ls                                           list your functions
  keys    create APP [--name N] | ls APP | revoke KEYID
                                               manage per-function API keys (multiple per app)
  version                                      print platform version (from package.json)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	baseURL := ""
	tokenOverride := ""
	rest := args
	for len(rest) >= 2 && (rest[0] == "--url" || rest[0] == "--token") {
		if rest[0] == "--url" {
			baseURL = rest[1]
		} else {
			tokenOverride = rest[1]
		}
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
	url := cli.ResolveURL(baseURL)
	token := cli.ResolveToken(tokenOverride)
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "version":
		fmt.Println(version.Version)
		return nil
	case "login":
		return cmdLogin(cmdArgs)
	case "logout":
		return cmdLogout(url, token)
	case "whoami":
		return cmdWhoami(url, token)
	case "config":
		return cmdConfig(cmdArgs)
	case "init":
		return cmdInit(cmdArgs)
	case "dev":
		return cmdDev(cmdArgs, url)
	case "deploy":
		return cmdDeploy(cmdArgs, url, token)
	case "invoke":
		return cmdInvoke(cmdArgs, url, token)
	case "logs":
		return cmdLogs(cmdArgs, url, token)
	case "ls":
		return cmdLs(url, token)
	case "keys":
		return cmdKeys(cmdArgs, url, token)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "control plane base")
	if err := fs.Parse(args); err != nil {
		return err
	}
	url := cli.ResolveURL(*urlFlag)
	fmt.Printf("control plane URL [%s]: ", url)
	if line, err := cli.Prompt(""); err == nil && strings.TrimSpace(line) != "" {
		url = strings.TrimSuffix(strings.TrimSpace(line), "/")
	}
	fmt.Println("contacting", url, "...")
	host, _ := os.Hostname()
	start, err := cli.DeviceStartReq(url, "cli@"+host)
	if err != nil {
		return fmt.Errorf("login: %v (is the control plane reachable at %s?)", err, url)
	}
	fmt.Printf("\nVerify this device:\n  %s%s\n", url, start.VerifyPath)
	fmt.Printf("Code: %s (expires in %d min)\nWaiting for browser verification...\n", start.UserCode, start.ExpiresIn/60)
	deadline := time.Now().Add(time.Duration(start.ExpiresIn+30) * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("login expired — run `actions login` again")
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
			fmt.Printf("Logged in as %s.\n", poll.Username)
			return nil
		case "denied":
			return fmt.Errorf("login denied")
		case "expired":
			return fmt.Errorf("login expired — run `actions login` again")
		}
	}
}

func cmdLogout(url, token string) error {
	cli.LogoutRemote(url, token)
	if err := cli.ClearAuth(); err != nil {
		return err
	}
	fmt.Println("logged out")
	return nil
}

func cmdWhoami(url, token string) error {
	me, err := cli.Me(url, token)
	if err != nil {
		return err
	}
	if me.Setup {
		fmt.Println("no users yet — run `actions login` to create the first account")
		return nil
	}
	fmt.Printf("%s\n", me.Name)
	return nil
}

func cmdConfig(args []string) error {
	if len(args) == 0 {
		a, _ := cli.LoadAuth()
		u := a.URL
		if u == "" {
			u = cli.ResolveURL("")
		}
		fmt.Printf("url=%s\n", u)
		if a.Username != "" {
			fmt.Printf("user=%s\n", a.Username)
		}
		return nil
	}
	if args[0] == "get" && len(args) == 2 && args[1] == "url" {
		fmt.Println(cli.ResolveURL(""))
		return nil
	}
	if args[0] == "set" && len(args) == 3 && args[1] == "url" {
		url := strings.TrimSuffix(args[2], "/")
		a, _ := cli.LoadAuth()
		a.URL = url
		return cli.SaveAuth(a)
	}
	return fmt.Errorf("usage: actions config [get url|set url VALUE]")
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

func cmdDeploy(args []string, baseURL, token string) error {
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
	res, err := cli.DeployWithAuth(*url, token, cfg.Name, string(tomlRaw), zipBytes)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s image=%s sha256=%.12s\n", res.Name, res.Version, res.Status, res.Image, res.SHA256)
	return nil
}

func cmdInvoke(args []string, baseURL, token string) error {
	fs := flag.NewFlagSet("invoke", flag.ContinueOnError)
	method := fs.String("method", "", "HTTP method (default POST with -d, else GET)")
	data := fs.String("data", "", "request body")
	fs.StringVar(data, "d", "", "request body (shorthand)")
	url := fs.String("url", baseURL, "control plane base")
	apiKey := fs.String("api-key", os.Getenv("ACTIONS_API_KEY"), "API key for key-protected functions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: actions invoke NAME[/path] (or NAMESPACE/NAME[/path])")
	}
	m := *method
	if m == "" {
		if *data != "" {
			m = "POST"
		} else {
			m = "GET"
		}
	}
	res, err := cli.InvokeWithAuth(*url, token, *apiKey, fs.Arg(0), m, []byte(*data))
	if err != nil {
		return err
	}
	fmt.Printf("status: %d\n%s\n", res.Status, res.Body)
	return nil
}

func cmdLogs(args []string, baseURL, token string) error {
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
		lines, err := cli.GetLogsWithAuth(*url, token, name)
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

func cmdLs(baseURL, token string) error {
	fns, err := cli.ListFunctionsWithAuth(baseURL, token)
	if err != nil {
		return err
	}
	if len(fns) == 0 {
		fmt.Println("(no functions)")
		return nil
	}
	fmt.Print(cli.FormatFunctions(fns, time.Now()))
	return nil
}

func cmdKeys(args []string, baseURL, token string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: actions keys create APP [--name N] | ls APP | revoke KEYID")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
		name := fs.String("name", "", "key label")
		url := fs.String("url", baseURL, "control plane base")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("usage: actions keys create APP [--name N]")
		}
		created, err := cli.CreateAPIKey(*url, token, fs.Arg(0), *name)
		if err != nil {
			return err
		}
		fmt.Printf("id=%s prefix=%s\nkey=%s\n(shown once — store it now)\n", created.ID, created.Prefix, created.Key)
		return nil
	case "ls", "list":
		if len(args) < 2 {
			return fmt.Errorf("usage: actions keys ls APP")
		}
		keys, err := cli.ListAPIKeys(baseURL, token, args[1])
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Println("(no keys)")
			return nil
		}
		for _, k := range keys {
			rev := ""
			if k.RevokedAt != "" {
				rev = " revoked=" + k.RevokedAt
			}
			fmt.Printf("%s  name=%q prefix=%s created=%s last_used=%s%s\n", k.ID, k.Name, k.Prefix, k.CreatedAt, k.LastUsedAt, rev)
		}
		return nil
	case "revoke", "rm", "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: actions keys revoke KEYID")
		}
		id := args[len(args)-1]
		if err := cli.RevokeAPIKey(baseURL, token, id); err != nil {
			return err
		}
		fmt.Println("revoked", id)
		return nil
	default:
		return fmt.Errorf("usage: actions keys create APP [--name N] | ls APP | revoke KEYID")
	}
}
