package devmock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestMockKVRoundTrip(t *testing.T) {
	m := New()
	url, close, err := m.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer close()

	val := base64.StdEncoding.EncodeToString([]byte("hello"))
	body, _ := json.Marshal(map[string]any{"function_name": "f", "key": "k", "value": val})
	resp, err := http.Post(url+"/sidecar/kv/put", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put = %d", resp.StatusCode)
	}
	if _, found := m.Server.KV.Get("f", "k"); !found {
		t.Fatal("not in mock KV")
	}
	_ = context.Background
}
