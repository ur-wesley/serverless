package bus

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemoryPubSub(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	var mu sync.Mutex
	var got [][]byte
	unsub, err := b.Subscribe("orders.created", func(_ context.Context, msg []byte) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(context.Background(), "orders.created", []byte("m1")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || string(got[0]) != "m1" {
		t.Fatalf("got %q", got)
	}
	unsub()
}
