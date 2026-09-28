// echo-go: queue subscriber + KV demo. Hand-rolled JSON (stdlib only);
// talks to the sidecar REST API via $ACTIONS_SIDECAR_URL when set.
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

func b64encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
func b64decode(s string) []byte {
	if s == "" {
		return nil
	}
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
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

func sidecarPost(sidecar, path string, body any) {
	if sidecar == "" {
		return
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", sidecar+path, bytes.NewReader(raw))
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

func sidecarGet(sidecar, fn, key string) (string, bool) {
	if sidecar == "" {
		return "", false
	}
	raw, _ := json.Marshal(map[string]string{"function_name": fn, "key": key})
	req, _ := http.NewRequest("POST", sidecar+"/sidecar/kv/get", bytes.NewReader(raw))
	if req == nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var out struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false
	}
	return string(b64decode(out.Value)), out.Found
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		var in invokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
			writeResp(w, 400, "bad invoke JSON")
			return
		}
		body := b64decode(in.Event.Body)
		fn := in.Ctx.FunctionName
		var text string
		if in.Event.Method == "QUEUE" {
			topic := in.Event.Query["topic"]
			sidecarPost(in.Ctx.SidecarURL, "/sidecar/kv/put", map[string]any{
				"function_name": fn, "key": "last",
				"value": b64encode(body), "ttl_seconds": 0,
			})
			sidecarPost(in.Ctx.SidecarURL, "/sidecar/log/append", map[string]string{
				"function_name": fn, "version": in.Ctx.Version,
				"request_id": in.Ctx.RequestID,
				"line":       fmt.Sprintf("queue got %d bytes on %s", len(body), topic),
			})
			text = fmt.Sprintf("echo QUEUE topic=%s body=%s", topic, string(body))
		} else {
			text = fmt.Sprintf("echo %s %s body=%s", in.Event.Method, in.Event.Path, string(body))
			if last, ok := sidecarGet(in.Ctx.SidecarURL, fn, "last"); ok {
				text += " last=" + last
			}
		}
		writeResp(w, 200, text)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	slog.Info("echo-go listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		slog.Error("exited", "err", err)
		os.Exit(1)
	}
}

func writeResp(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": status, "headers": map[string]string{"content-type": "text/plain"},
		"body": b64encode([]byte(text)),
	})
}
