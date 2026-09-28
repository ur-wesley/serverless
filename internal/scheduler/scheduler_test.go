package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"actions/internal/bus"
	"actions/internal/runner"
	"actions/internal/store"
)

type fakeBackend struct {
	mu     sync.Mutex
	calls  []string
	bodies [][]byte
}

func (f *fakeBackend) Invoke(_ context.Context, ref runner.FunctionRef, payload []byte) (*runner.InvokeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ref.Name)
	f.bodies = append(f.bodies, payload)
	return &runner.InvokeResponse{Status: 200}, nil
}

func (f *fakeBackend) Close() {}

func (f *fakeBackend) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testStore(t *testing.T, name, toml string) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertFunction(name, "v1", toml); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestQueueTriggerInvokes(t *testing.T) {
	st := testStore(t, "echo", "name=\"echo\"\nruntime=\"go\"\nqueue=[\"orders.created\"]\n")
	b := bus.NewMemory()
	defer b.Close()
	be := &fakeBackend{}
	s := New(st, be, b, "", "")
	s.Sync()
	defer s.Stop()
	if err := b.Publish(context.Background(), "orders.created", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for be.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if be.count() != 1 {
		t.Fatalf("invocations = %d, want 1", be.count())
	}
}

func TestBadCronSkipped(t *testing.T) {
	st := testStore(t, "bad", "name=\"bad\"\nruntime=\"go\"\ncron=[\"not a spec\"]\n")
	b := bus.NewMemory()
	defer b.Close()
	s := New(st, be(), b, "", "")
	s.Sync()
	defer s.Stop()
	if n := len(s.cron.Entries()); n != 0 {
		t.Fatalf("entries = %d, want 0", n)
	}
}

func be() *fakeBackend { return &fakeBackend{} }

func TestCronSpecAccepted(t *testing.T) {
	st := testStore(t, "tick", "name=\"tick\"\nruntime=\"go\"\ncron=[\"*/5 * * * *\"]\n")
	b := bus.NewMemory()
	defer b.Close()
	s := New(st, be(), b, "", "")
	s.Sync()
	defer s.Stop()
	if n := len(s.cron.Entries()); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
}
