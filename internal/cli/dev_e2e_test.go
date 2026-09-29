// End-to-end tests for `oort dev`: spawn a real handler via `go run`
// behind the dev gateway and assert the browser-facing surface.
//
// Portable by design (runs on linux/darwin/windows CI): stdlib-only Go
// fixture, 127.0.0.1 networking, Port 0 (free port), no shell, no bun.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const devE2EActionsTOML = `name = "e2e"
runtime = "go"
route = "/f/e2e"
timeout_ms = 10000
memory_mb = 256
allow_egress = false
`

const devE2EGoMod = `module devfixture

go 1.27
`

// Fixture handler: minimal ABI echo. BOOT is random per process so the
// watch test can detect restarts; body echoes METHOD PATH raw-body.
const devE2EMainGo = `package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

var bootID = func() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}()

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Event struct {
				Method string "json:\"method\""
				Path   string "json:\"path\""
				Body   string "json:\"body\""
			} "json:\"event\""
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req)
		raw, _ := base64.StdEncoding.DecodeString(req.Event.Body)
		text := fmt.Sprintf("e2e BOOT=%s %s %s body=%s", bootID, req.Event.Method, req.Event.Path, string(raw))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  200,
			"headers": map[string]string{"content-type": "text/plain"},
			"body":    base64.StdEncoding.EncodeToString([]byte(text)),
		})
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	_ = http.ListenAndServe("127.0.0.1:"+port, mux)
}
`

func writeDevE2EFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"actions.toml": devE2EActionsTOML,
		"go.mod":       devE2EGoMod,
		"main.go":      devE2EMainGo,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// startDevE2E runs DevWithOptions in the background and returns the bound
// gateway URL once the listener is up. Caller must cancel to shut down.
func startDevE2E(t *testing.T, ctx context.Context, dir string, watch bool) (devURL string, done chan error) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required for dev e2e test")
	}
	t.Setenv("GOTOOLCHAIN", "local") // never hit the network for toolchains
	ready := make(chan string, 1)
	done = make(chan error, 1)
	go func() {
		done <- DevWithOptions(ctx, DevOptions{
			Dir: dir, Offline: true, Port: 0, Watch: watch,
			OnReady: func(url string) { ready <- url },
		})
	}()
	select {
	case devURL = <-ready:
		return devURL, done
	case err := <-done:
		t.Fatalf("dev exited before gateway ready: %v", err)
	case <-time.After(120 * time.Second):
		t.Fatal("timeout waiting for dev gateway listener")
	}
	return "", nil
}

func pollHealthy(t *testing.T, client *http.Client, devURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(devURL + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 && string(body) == "ok" {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("handler behind %s not healthy after %s", devURL, timeout)
}

func getE2E(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, string(raw)
}

func bootToken(body string) string {
	// body: "e2e BOOT=<hex> METHOD PATH body=..."
	fields := strings.Fields(body)
	for _, f := range fields {
		if v, ok := strings.CutPrefix(f, "BOOT="); ok {
			return v
		}
	}
	return ""
}

func TestDevEndToEnd(t *testing.T) {
	dir := writeDevE2EFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &http.Client{Timeout: 15 * time.Second}
	devURL, done := startDevE2E(t, ctx, dir, false)
	pollHealthy(t, client, devURL, 90*time.Second)

	// Index page.
	if code, body := getE2E(t, client, devURL+"/"); code != 200 || !strings.Contains(body, "oort dev") {
		t.Fatalf("GET / = %d %q", code, body)
	}
	// /f/<name>/... translation.
	code, fbody := getE2E(t, client, devURL+"/f/e2e/hi")
	if code != 200 || !strings.HasSuffix(fbody, "GET /hi body=") || bootToken(fbody) == "" {
		t.Fatalf("GET /f/e2e/hi = %d %q", code, fbody)
	}
	// Bare path translation.
	if code, body := getE2E(t, client, devURL+"/hi"); code != 200 || !strings.HasSuffix(body, "GET /hi body=") {
		t.Fatalf("GET /hi = %d %q", code, body)
	}
	if _, body := getE2E(t, client, devURL+"/hi"); bootToken(body) == "" {
		t.Fatalf("missing BOOT token in %q", body)
	}
	// POST with body echo.
	resp, err := client.Post(devURL+"/f/e2e/echo", "text/plain", strings.NewReader("ping"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasSuffix(string(raw), "POST /echo body=ping") {
		t.Fatalf("POST /f/e2e/echo = %d %q", resp.StatusCode, raw)
	}
	// Raw ABI passthrough.
	payload, err := buildInvokeRequest("GET", "/raw", nil, nil, []byte("x"), "e2e", "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Post(devURL+"/invoke", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	status, _, outBody, err := parseInvokeResponse(raw)
	if err != nil || status != 200 || !strings.HasSuffix(string(outBody), "GET /raw body=x") {
		t.Fatalf("POST /invoke = %d %q err=%v", status, outBody, err)
	}

	// Shutdown: gateway goes down, Dev returns context.Canceled.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dev returned %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("dev did not shut down after cancel")
	}
	if _, err := client.Get(devURL + "/"); err == nil {
		t.Fatal("gateway still serving after cancel")
	}
}

func TestDevWatchRestart(t *testing.T) {
	dir := writeDevE2EFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &http.Client{Timeout: 15 * time.Second}
	devURL, done := startDevE2E(t, ctx, dir, true)
	pollHealthy(t, client, devURL, 90*time.Second)

	_, before := getE2E(t, client, devURL+"/f/e2e/hi")
	beforeBoot := bootToken(before)
	if beforeBoot == "" {
		t.Fatalf("missing BOOT token in %q", before)
	}

	// Touch a watched file -> supervisor restarts the handler -> new BOOT.
	f, err := os.OpenFile(filepath.Join(dir, "main.go"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "\n// touch %d\n", time.Now().UnixNano())
	f.Close()

	deadline := time.Now().Add(120 * time.Second)
	for {
		// Soft GET: mid-restart the gateway answers 502 while the new
		// handler boots; only a changed BOOT token counts as restarted.
		var body string
		if resp, err := client.Get(devURL + "/f/e2e/hi"); err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			body = string(raw)
		}
		if b := bootToken(body); b != "" && b != beforeBoot {
			break // restarted
		}
		if time.Now().After(deadline) {
			t.Fatalf("handler did not restart within 120s (still BOOT=%s)", beforeBoot)
		}
		time.Sleep(1 * time.Second)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("dev did not shut down after cancel")
	}
}
