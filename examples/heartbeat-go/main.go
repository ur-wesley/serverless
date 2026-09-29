// heartbeat-go: cron writer + HTTP reader demo. Every minute the scheduler
// invokes it with method CRON; it bumps a counter in KV (sidecar) that the
// HTTP path (and the ui-go dashboard) reads back.
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
	"strconv"
	"time"
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

func tokenHeader(req *http.Request) {
	if tok := os.Getenv("ACTIONS_SIDECAR_TOKEN"); tok != "" {
		req.Header.Set("X-Actions-Token", tok)
	}
}

func kvPut(sidecar, fn, key, value string) {
	if sidecar == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"function_name": fn, "key": key,
		"value": b64encode([]byte(value)), "ttl_seconds": 0,
	})
	req, err := http.NewRequest("POST", sidecar+"/sidecar/kv/put", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	tokenHeader(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
}

func kvGet(sidecar, fn, key string) (string, bool) {
	if sidecar == "" {
		return "", false
	}
	raw, _ := json.Marshal(map[string]string{"function_name": fn, "key": key})
	req, err := http.NewRequest("POST", sidecar+"/sidecar/kv/get", bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	tokenHeader(req)
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
	tokenHeader(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": status, "headers": map[string]string{"content-type": "application/json"},
		"body": base64.StdEncoding.EncodeToString(body),
	})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		var in invokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad invoke JSON"})
			return
		}
		fn, sidecar := in.Ctx.FunctionName, in.Ctx.SidecarURL
		if in.Event.Method == "CRON" {
			n := 0
			if last, ok := kvGet(sidecar, fn, "count"); ok {
				n, _ = strconv.Atoi(last)
			}
			n++
			now := time.Now().UTC().Format(time.RFC3339)
			kvPut(sidecar, fn, "count", strconv.Itoa(n))
			kvPut(sidecar, fn, "at", now)
			sidecarLog(sidecar, fn, in.Ctx.Version, in.Ctx.RequestID, "cron beat")
			writeJSON(w, 200, map[string]any{"beat": n, "at": now})
			return
		}
		count, _ := kvGet(sidecar, fn, "count")
		at, _ := kvGet(sidecar, fn, "at")
		if count == "" {
			count = "0"
		}
		sidecarLog(sidecar, fn, in.Ctx.Version, in.Ctx.RequestID, "http read")
		writeJSON(w, 200, map[string]any{
			"fn": fn, "beats": count, "last_beat": at,
			"hint": fmt.Sprintf("cron writes every minute; %s beats so far", count),
		})
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	slog.Info("heartbeat-go listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		slog.Error("exited", "err", err)
		os.Exit(1)
	}
}
