// hello-go: minimal ABI handler in Go. No proto dependency;
// wire bodies are base64 (bytes fields in events.proto).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
)

func sidecarLog(sidecar, fn, ver, reqID, line string) {
	if sidecar == "" {
		return
	}
	raw, _ := json.Marshal(map[string]string{
		"function_name": fn, "version": ver,
		"request_id": reqID, "line": line,
	})
	req, err := http.NewRequest("POST", sidecar+"/sidecar/log/append", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
}

type httpEvent struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Query   map[string]string `json:"query"`
	Body    string            `json:"body"` // base64
}

type invokeRequest struct {
	Event struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Query   map[string]string `json:"query"`
		Body    string            `json:"body"`
	} `json:"event"`
	Ctx struct {
		FunctionName string `json:"function_name"`
		Version      string `json:"version"`
		RequestID    string `json:"request_id"`
		SidecarURL   string `json:"sidecar_url"`
	} `json:"ctx"`
}

type invokeResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		var req invokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
			writeJSON(w, invokeResponse{Status: 400, Headers: map[string]string{}, Body: b64("bad invoke JSON")})
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(req.Event.Body)
		text := fmt.Sprintf("hello from hello-go %s %s body=%s", req.Event.Method, req.Event.Path, string(raw))
		sidecarLog(req.Ctx.SidecarURL, req.Ctx.FunctionName, req.Ctx.Version, req.Ctx.RequestID,
			fmt.Sprintf("http %s %s body=%d bytes", req.Event.Method, req.Event.Path, len(raw)))
		writeJSON(w, invokeResponse{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: b64(text)})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	slog.Info("hello-go listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		slog.Error("exited", "err", err)
		os.Exit(1)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func writeJSON(w http.ResponseWriter, v invokeResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
