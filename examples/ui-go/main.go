// ui-go: serves the SolidJS dashboard (frontend/, built with
// `bun run build` into dist/) as static files. Build the frontend first:
//
//	cd frontend && bun install && bun run build
//
// Intercom: /api/* routes proxy through the sidecar using this function's
// own token. Requires actions.toml allow_logs/allow_invoke/allow_kv/
// allow_blobs (e.g. ["*"]) — same-owner only, enforced by the sidecar.
package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

//go:embed dist
var distFS embed.FS

type invokeRequest struct {
	Event struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Query   map[string]string `json:"query"`
		Body    string            `json:"body"` // base64
	} `json:"event"`
	Ctx struct {
		FunctionName string `json:"function_name"`
	} `json:"ctx"`
}

type invokeResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".ico":  "image/x-icon",
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		var req invokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
			writeResp(w, 400, "text/plain", "bad invoke JSON")
			return
		}
		p := path.Clean("/" + strings.TrimPrefix(req.Event.Path, "/"))
		if strings.HasPrefix(p, "/api/") {
			status, ct, body := handleAPI(req)
			writeResp(w, status, ct, body)
			return
		}
		if p == "/" {
			p = "/index.html"
		}
		raw, err := fs.ReadFile(distFS, "dist"+p)
		if err != nil {
			writeResp(w, 404, "text/plain", "not found: "+p)
			return
		}
		ct := contentTypes[path.Ext(p)]
		if ct == "" {
			ct = "application/octet-stream"
		}
		writeResp(w, 200, ct, string(raw))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	slog.Info("ui-go listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		slog.Error("exited", "err", err)
		os.Exit(1)
	}
}

func handleAPI(req invokeRequest) (int, string, string) {
	method := req.Event.Method
	p := path.Clean("/" + strings.TrimPrefix(req.Event.Path, "/"))
	q := req.Event.Query
	if q == nil {
		q = map[string]string{}
	}
	rawBody, _ := base64.StdEncoding.DecodeString(req.Event.Body)

	switch {
	case p == "/api/functions" && method == "GET":
		var out struct {
			Functions []string `json:"functions"`
		}
		if err := sidecarPost("/sidecar/functions/list", map[string]any{}, &out); err != nil {
			return 502, "text/plain", "functions: " + err.Error()
		}
		type fnInfo struct {
			Name string `json:"Name"`
		}
		list := make([]fnInfo, 0, len(out.Functions))
		for _, n := range out.Functions {
			list = append(list, fnInfo{Name: n})
		}
		raw, _ := json.Marshal(list)
		return 200, "application/json", string(raw)
	case p == "/api/logs" && method == "GET":
		fn := q["fn"]
		if fn == "" {
			return 400, "text/plain", "?fn= required"
		}
		var out struct {
			Lines []any `json:"lines"`
		}
		if err := sidecarPost("/sidecar/logs/tail", map[string]any{"target_function": fn, "limit": 100}, &out); err != nil {
			return mapSidecarError(err)
		}
		raw, _ := json.Marshal(out.Lines)
		if string(raw) == "null" {
			raw = []byte("[]")
		}
		return 200, "application/json", string(raw)
	case strings.HasPrefix(p, "/api/pub/") && method == "POST":
		topic := strings.TrimPrefix(p, "/api/pub/")
		if topic == "" || strings.Contains(topic, "/") {
			return 400, "text/plain", "bad topic"
		}
		if err := sidecarPost("/sidecar/queue/publish", map[string]any{
			"topic": topic, "message": base64.StdEncoding.EncodeToString(rawBody),
		}, nil); err != nil {
			return mapSidecarError(err)
		}
		return 202, "text/plain", "accepted"
	case strings.HasPrefix(p, "/api/invoke/") && method == "POST":
		target := strings.TrimPrefix(p, "/api/invoke/")
		target, _, _ = strings.Cut(target, "/")
		if target == "" {
			return 400, "text/plain", "bad target"
		}
		var out struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		}
		if err := sidecarPost("/sidecar/invoke", map[string]any{
			"target_function": target, "method": "POST", "path": "/",
			"body": base64.StdEncoding.EncodeToString(rawBody),
		}, &out); err != nil {
			return mapSidecarError(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(out.Body)
		ct := out.Headers["content-type"]
		if ct == "" {
			ct = "text/plain"
		}
		return out.Status, ct, string(raw)
	default:
		return 404, "text/plain", "unknown api: " + p
	}
}

func mapSidecarError(err error) (int, string, string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "403") || strings.Contains(msg, "forbidden"):
		return 403, "text/plain", "forbidden: allow_* in actions.toml + same owner required"
	case strings.Contains(msg, "404") || strings.Contains(msg, "unknown function"):
		return 404, "text/plain", "unknown function"
	default:
		return 502, "text/plain", msg
	}
}

func sidecarPost(path string, in any, out any) error {
	base := os.Getenv("ACTIONS_SIDECAR_URL")
	if base == "" {
		return errNoSidecar()
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequest("POST", base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return &sidecarError{status: resp.StatusCode, msg: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type sidecarError struct {
	status int
	msg    string
}

func (e *sidecarError) Error() string {
	if e.msg != "" {
		return "sidecar: " + e.msg
	}
	return "sidecar error"
}

func errNoSidecar() error { return &sidecarError{status: 500, msg: "no sidecar"} }

func writeResp(w http.ResponseWriter, status int, contentType, body string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(invokeResponse{
		Status:  status,
		Headers: map[string]string{"content-type": contentType},
		Body:    base64.StdEncoding.EncodeToString([]byte(body)),
	})
}
