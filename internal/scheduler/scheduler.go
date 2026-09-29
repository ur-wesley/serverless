// Package scheduler fires cron and queue triggers into the runner.
// Cron specs and queue topics come from actions.toml (cron/queue arrays).
// Triggers resolve through the same registry as HTTP, so deploys take
// effect on the next tick/message. Resyncs every 30s to pick up deploys.
package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/ur-wesley/oort/internal/bus"
	"github.com/ur-wesley/oort/internal/deploy"
	"github.com/ur-wesley/oort/internal/functions"
	"github.com/ur-wesley/oort/internal/runner"
	"github.com/ur-wesley/oort/internal/store"
)

type Scheduler struct {
	store      *store.Store
	backend    runner.Backend
	bus        bus.Bus
	helloImage string
	sidecarURL string

	mu       sync.Mutex
	cron     *cron.Cron
	cronKeys map[string]cron.EntryID // "fn\x00spec" -> entry
	unsubs   map[string]func()       // topic -> unsub (one shared sub per topic)
	subFns   map[string]string       // topic -> sorted fn list (detect membership change)
}

func New(st *store.Store, backend runner.Backend, b bus.Bus, helloImage, sidecarURL string) *Scheduler {
	return &Scheduler{
		store: st, backend: backend, bus: b,
		helloImage: helloImage, sidecarURL: sidecarURL,
		cron:     cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger))),
		cronKeys: map[string]cron.EntryID{},
		unsubs:   map[string]func(){},
		subFns:   map[string]string{},
	}
}

func (s *Scheduler) Start() {
	s.cron.Start()
	s.Sync()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			s.Sync()
		}
	}()
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, unsub := range s.unsubs {
		unsub()
	}
	s.unsubs = map[string]func(){}
	s.subFns = map[string]string{}
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// Sync reconciles cron entries and queue subscriptions with the registry.
func (s *Scheduler) Sync() {
	fns, err := s.store.ListFunctions()
	if err != nil {
		slog.Warn("scheduler sync: list failed", "err", err)
		return
	}
	wantCron := map[string]cronSpec{}
	wantQueue := map[string][]string{} // topic -> [fn...]
	for _, fn := range fns {
		if fn.ActiveVersion == "" {
			continue
		}
		cfg, err := deploy.ParseConfig(fn.ConfigTOML)
		if err != nil {
			slog.Warn("scheduler sync: bad config", "fn", fn.Name, "err", err)
			continue
		}
		for _, spec := range cfg.Cron {
			wantCron[fn.Name+"\x00"+spec] = cronSpec{fn: fn.Name, spec: spec}
		}
		for _, topic := range cfg.Queue {
			if strings.TrimSpace(topic) == "" {
				continue
			}
			wantQueue[topic] = append(wantQueue[topic], fn.Name)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, spec := range wantCron {
		if _, ok := s.cronKeys[key]; ok {
			continue
		}
		id, err := s.cron.AddFunc(spec.spec, func() { s.fireCron(spec.fn) })
		if err != nil {
			slog.Warn("bad cron spec, skipping", "fn", spec.fn, "spec", spec.spec, "err", err)
			continue
		}
		s.cronKeys[key] = id
		slog.Info("cron scheduled", "fn", spec.fn, "spec", spec.spec)
	}
	for key, id := range s.cronKeys {
		if _, ok := wantCron[key]; !ok {
			s.cron.Remove(id)
			delete(s.cronKeys, key)
		}
	}
	for topic, fns := range wantQueue {
		members := strings.Join(sortedCopy(fns), ",")
		if unsub, ok := s.unsubs[topic]; ok {
			if s.subFns[topic] == members {
				continue
			}
			unsub()
			delete(s.unsubs, topic)
			delete(s.subFns, topic)
			slog.Info("queue resubscribing (members changed)", "topic", topic, "fns", fns)
		}
		fnsCopy := append([]string{}, fns...)
		unsub, err := s.bus.Subscribe(topic, func(ctx context.Context, msg []byte) error {
			var wg sync.WaitGroup
			errCh := make(chan error, len(fnsCopy))
			for _, fn := range fnsCopy {
				wg.Add(1)
				go func(fn string) {
					defer wg.Done()
					if err := s.fireQueue(fn, topic, msg); err != nil {
						errCh <- err
					}
				}(fn)
			}
			wg.Wait()
			close(errCh)
			for err := range errCh {
				return err
			}
			return nil
		})
		if err != nil {
			slog.Warn("queue subscribe failed", "topic", topic, "err", err)
			continue
		}
		s.unsubs[topic] = unsub
		s.subFns[topic] = members
		slog.Info("queue subscribed", "topic", topic, "fns", fns)
	}
	for topic, unsub := range s.unsubs {
		if _, ok := wantQueue[topic]; !ok {
			unsub()
			delete(s.unsubs, topic)
			delete(s.subFns, topic)
		}
	}
}

type cronSpec struct {
	fn   string
	spec string
}

func (s *Scheduler) fireCron(fn string) {
	// System trigger: exact registry entry, no dev "hello" fallback.
	info, ok := functions.ResolveFull(s.store, fn, s.sidecarURL)
	if !ok {
		return
	}
	ref := info.Ref
	payload, _ := json.Marshal(invokePayload("CRON", "/cron", nil, nil, ref))
	ctx, cancel := context.WithTimeout(context.Background(), runner.ClampTimeout(ref.Timeout)+15*time.Second)
	defer cancel()
	resp, err := s.backend.Invoke(ctx, ref, payload)
	if err != nil {
		slog.Warn("cron invoke failed", "fn", fn, "err", err)
		return
	}
	slog.Info("cron invoke ok", "fn", fn, "status", resp.Status)
}

func (s *Scheduler) fireQueue(fn, topic string, msg []byte) error {
	// System trigger: exact registry entry, no dev "hello" fallback.
	info, ok := functions.ResolveFull(s.store, fn, s.sidecarURL)
	if !ok {
		return fmt.Errorf("unknown function %q", fn)
	}
	ref := info.Ref
	payload, _ := json.Marshal(invokePayload("QUEUE", "/"+topic, nil, map[string]string{"topic": topic}, ref, msg))
	ctx, cancel := context.WithTimeout(context.Background(), runner.ClampTimeout(ref.Timeout)+15*time.Second)
	defer cancel()
	resp, err := s.backend.Invoke(ctx, ref, payload)
	if err != nil {
		slog.Warn("queue invoke failed", "fn", fn, "topic", topic, "err", err)
		return err
	}
	slog.Info("queue invoke ok", "fn", fn, "topic", topic, "status", resp.Status)
	if resp.Status >= 500 {
		return fmt.Errorf("handler %q returned %d", fn, resp.Status)
	}
	return nil
}

func invokePayload(method, path string, headers, query map[string]string, ref runner.FunctionRef, body ...[]byte) map[string]any {
	var raw []byte
	if len(body) > 0 {
		raw = body[0]
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if query == nil {
		query = map[string]string{}
	}
	return map[string]any{
		"event": map[string]any{
			"method": method, "path": path,
			"headers": headers, "query": query,
			"body": base64.StdEncoding.EncodeToString(raw),
		},
		"ctx": map[string]any{
			"function_name": ref.Name, "version": ref.Version,
			"request_id":  requestID(),
			"deadline_ms": time.Now().Add(runner.ClampTimeout(ref.Timeout)).UnixMilli(),
			"sidecar_url": ref.SidecarURL,
		},
	}
}

func requestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
