package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ur-wesley/oort/internal/bus"
	"github.com/ur-wesley/oort/internal/sidecar"
)

func TestInvokeRoundTrip(t *testing.T) {
	h := NewHandler(func(_ context.Context, c Ctx, e Event) Response {
		if c.FunctionName != "f" || string(e.Body) != "ping" {
			t.Errorf("got ctx=%+v event=%+v", c, e)
		}
		return Response{Status: 201, Headers: map[string]string{"x-a": "b"}, Body: []byte("pong")}
	})
	body := `{"event":{"method":"GET","path":"/x","headers":{},"query":{},"body":"` +
		base64.StdEncoding.EncodeToString([]byte("ping")) + `"},` +
		`"ctx":{"function_name":"f","version":"v1","request_id":"r","deadline_ms":1}}`
	req := httptest.NewRequest("POST", "/invoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("outer status = %d", res.StatusCode)
	}
	var out struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Status != 201 || out.Headers["x-a"] != "b" {
		t.Fatalf("out = %+v", out)
	}
	raw, _ := base64.StdEncoding.DecodeString(out.Body)
	if string(raw) != "pong" {
		t.Fatalf("body = %q", raw)
	}
}

func TestEnvKVBlobAgainstSidecar(t *testing.T) {
	arts := &mapStore{data: map[string][]byte{}}
	srv := &sidecar.Server{KV: sidecar.NewMemKV(), Blobs: &sidecar.LocalBlob{}, Bus: bus.NewMemory(), Logs: sidecar.NewLogRing()}
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	sidecar.RegisterTransferRoutes(mux, arts, nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	srv.Blobs = &sidecar.LocalBlob{ControlURL: ts.URL}

	env := Env{BaseURL: ts.URL, Client: ts.Client()}
	ctx := context.Background()
	if err := env.KVPut(ctx, "f", "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	got, found, err := env.KVGet(ctx, "f", "k")
	if err != nil || !found || string(got) != "v" {
		t.Fatalf("kv = %q %v %v", got, found, err)
	}
	if err := env.BlobPutBytes(ctx, "f", "a.bin", []byte("blobdata")); err != nil {
		t.Fatal(err)
	}
	raw, err := env.BlobGetBytes(ctx, "f", "a.bin")
	if err != nil || string(raw) != "blobdata" {
		t.Fatalf("blob = %q %v", raw, err)
	}
	if err := env.QueuePublish(ctx, "t", []byte("m")); err != nil {
		t.Fatal(err)
	}
	if err := env.Logf(ctx, "f", "v1", "r", "line %d", 1); err != nil {
		t.Fatal(err)
	}
	if tail := srv.Logs.Tail("f", 1); len(tail) != 1 {
		t.Fatalf("logs = %+v", tail)
	}
}

type mapStore struct{ data map[string][]byte }

func (m *mapStore) Put(_ context.Context, key string, data []byte) error {
	m.data[key] = data
	return nil
}

func (m *mapStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	return v, nil
}

func (m *mapStore) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}
