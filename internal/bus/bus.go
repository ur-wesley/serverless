// Package bus delivers queue messages to subscriber functions.
// NATS JetStream (stream "events", subjects events.>) when NATS_URL is
// reachable; otherwise an in-process fallback so `actions dev` and tests
// run without infrastructure. Topic "orders.created" maps to "events.orders.created".
package bus

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Bus interface {
	Publish(ctx context.Context, topic string, msg []byte) error
	Subscribe(topic string, fn func(ctx context.Context, msg []byte) error) (unsub func(), err error)
	Close()
}

func subject(topic string) string { return "events." + sanitize(topic) }

func sanitize(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// NewFromEnv dials NATS_URL (3s timeout) or falls back to memory.
func NewFromEnv() Bus {
	url := os.Getenv("NATS_URL")
	if url == "" {
		return NewMemory()
	}
	nc, err := nats.Connect(url, nats.Timeout(3*time.Second))
	if err != nil {
		slog.Warn("nats unavailable, using in-process bus", "url", url, "err", err)
		return NewMemory()
	}
	nb, err := newNats(nc)
	if err != nil {
		slog.Warn("jetstream unavailable, using in-process bus", "err", err)
		nc.Close()
		return NewMemory()
	}
	return nb
}

// --- NATS ---

type natsBus struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	stream jetstream.Stream
	mu     sync.Mutex
	subs   []jetstream.ConsumeContext
}

func newNats(nc *nats.Conn) (*natsBus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "events",
		Subjects: []string{"events.>"},
	})
	if err != nil {
		return nil, err
	}
	return &natsBus{nc: nc, js: js, stream: stream}, nil
}

func (n *natsBus) Publish(ctx context.Context, topic string, msg []byte) error {
	_, err := n.js.Publish(ctx, subject(topic), msg)
	return err
}

func (n *natsBus) Subscribe(topic string, fn func(ctx context.Context, msg []byte) error) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Stable durable name per topic: restarts/resyncs resume from the last
	// acked message instead of replaying stream history (single-node).
	cons, err := n.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          "fn-" + strings.ReplaceAll(sanitize(topic), ".", "-"),
		Durable:       "fn-" + strings.ReplaceAll(sanitize(topic), ".", "-"),
		FilterSubject: subject(topic),
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if err := fn(context.Background(), m.Data()); err != nil {
			slog.Warn("queue handler failed, nacking for redelivery", "topic", topic, "err", err)
			_ = m.Nak()
			return
		}
		_ = m.Ack()
	})
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.subs = append(n.subs, cc)
	n.mu.Unlock()
	return func() { cc.Stop() }, nil
}

func (n *natsBus) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, cc := range n.subs {
		cc.Stop()
	}
	n.nc.Drain()
}

// --- memory ---

type memBus struct {
	mu   sync.RWMutex
	subs map[string][]func(ctx context.Context, msg []byte) error
}

func NewMemory() Bus { return &memBus{subs: map[string][]func(context.Context, []byte) error{}} }

func (m *memBus) Publish(ctx context.Context, topic string, msg []byte) error {
	m.mu.RLock()
	fns := append([]func(context.Context, []byte) error{}, m.subs[topic]...)
	m.mu.RUnlock()
	for _, fn := range fns {
		go func() {
			if err := fn(ctx, msg); err != nil {
				slog.Warn("queue handler failed", "topic", topic, "err", err)
			}
		}()
	}
	return nil
}

func (m *memBus) Subscribe(topic string, fn func(ctx context.Context, msg []byte) error) (func(), error) {
	m.mu.Lock()
	m.subs[topic] = append(m.subs[topic], fn)
	idx := len(m.subs[topic]) - 1
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.subs[topic]
		if idx < len(s) {
			m.subs[topic] = append(s[:idx], s[idx+1:]...)
		}
	}, nil
}

func (m *memBus) Close() {}
