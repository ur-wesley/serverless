package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"actions/internal/deploy"
	"actions/internal/devmock"
)

// defaultHandlerCmd guesses how to run the function locally.
func defaultHandlerCmd(runtime, dir string, extra []string) []string {
	if len(extra) > 0 {
		return extra
	}
	switch runtime {
	case "ts":
		return []string{"bun", "src/index.ts"}
	default:
		return []string{"go", "run", "."}
	}
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

// Dev runs the handler locally with PORT + ACTIONS_SIDECAR_URL set.
// offline=true starts an in-memory sidecar mock; otherwise the sidecar
// URL points at the control plane (url) so KV/Blob/Queue hit real backends.
func Dev(ctx context.Context, dir, url string, offline bool, cmdArgs []string) error {
	tomlRaw, err := os.ReadFile(filepath.Join(dir, "actions.toml"))
	if err != nil {
		return fmt.Errorf("read actions.toml: %w", err)
	}
	cfg, err := deploy.ParseConfig(string(tomlRaw))
	if err != nil {
		return err
	}
	sidecarURL := url
	var closeMock func()
	if offline {
		m := devmock.New()
		mockURL, close, err := m.Listen()
		if err != nil {
			return fmt.Errorf("mock sidecar: %w", err)
		}
		closeMock = close
		defer close()
		sidecarURL = mockURL
		fmt.Printf("mock sidecar at %s\n", mockURL)
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	argv := defaultHandlerCmd(cfg.Runtime, dir, cmdArgs)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PORT="+port, "ACTIONS_SIDECAR_URL="+sidecarURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %v: %w", argv, err)
	}
	fmt.Printf("%s dev on http://127.0.0.1:%s (sidecar %s)\n", cfg.Name, port, sidecarURL)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	select {
	case err := <-done:
		if closeMock != nil {
			closeMock()
		}
		return err
	case <-sig:
		_ = cmd.Process.Kill()
		<-done
		if closeMock != nil {
			closeMock()
		}
		return context.Canceled
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}
