// ui-go: serves the SolidJS dashboard (frontend/, built with
// `bun run build` into dist/) as static files. Build the frontend first:
//
//	cd frontend && bun install && bun run build
package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
)

//go:embed dist
var distFS embed.FS

type invokeRequest struct {
	Event struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"event"`
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

func writeResp(w http.ResponseWriter, status int, contentType, body string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(invokeResponse{
		Status:  status,
		Headers: map[string]string{"content-type": contentType},
		Body:    base64.StdEncoding.EncodeToString([]byte(body)),
	})
}
