// ui-go: serves a static SolidJS single-page view (embedded index.html).
// The SolidJS app boots in the browser via CDN ESM imports (no build step);
// the handler itself just returns text/html.
package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
)

//go:embed index.html
var page []byte

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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(invokeResponse{
			Status:  200,
			Headers: map[string]string{"content-type": "text/html; charset=utf-8"},
			Body:    base64.StdEncoding.EncodeToString(page),
		})
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
