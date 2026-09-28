// Package runner implements the docker-runsc backend: each function version
// runs as a sandboxed container serving GET /healthz + POST /invoke.
//
// Isolation model:
//   - allow_egress=true (or dev without SandboxNet): shared network,
//     published 127.0.0.1:<port> -> :8080, invoked via HostGW.
//   - allow_egress=false with Config.SandboxNet: container joins the
//     pre-created --internal network, no published ports, invoked via
//     container DNS. External egress (incl. DNS) is blocked by the daemon;
//     only hosts on the sandbox network (control plane/sidecar) are
//     reachable. Requires the control plane in-container (compose).
//
// Hardening per container: read-only rootfs, 64MB /tmp, cap-drop ALL,
// no-new-privileges, memory+swap capped, 0.5 CPU, pids-limit 128,
// nofile 256, actions-managed labels, per-instance sidecar token.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"actions/internal/artifacts"
)

const containerPort = "8080"

type instance struct {
	containerID string
	fn          string
	name        string
	base        string // http://host:port or http://cname:8080
	token       string
	lastUsed    time.Time
}

type DockerRunner struct {
	cfg Config

	mu        sync.Mutex
	instances map[string]*instance
	starting  map[string]chan struct{} // single-flight cold starts per function key

	useRunsc *bool // nil = unprobed; probed lazily, falls back on unknown runtime

	client   *http.Client
	reapStop chan struct{}
}

func NewDockerRunner(cfg Config) *DockerRunner {
	if cfg.Network == "" {
		cfg.Network = "actions-net"
	}
	if cfg.HostGW == "" {
		cfg.HostGW = "127.0.0.1"
	}
	r := &DockerRunner{
		cfg:       cfg,
		instances: map[string]*instance{},
		starting:  map[string]chan struct{}{},
		client:    &http.Client{},
		reapStop:  make(chan struct{}),
	}
	r.pruneOrphans()
	go r.reapLoop()
	return r
}

// pruneOrphans removes containers from previous control-plane lives.
func (r *DockerRunner) pruneOrphans() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=actions-managed=true").CombinedOutput()
	if err != nil {
		slog.Warn("orphan reap: docker unavailable", "err", err)
		return
	}
	for _, id := range strings.Fields(string(out)) {
		slog.Info("removing orphan handler", "container", shortID(id))
		_ = r.dockerKill(id)
	}
}

func keyFor(ref FunctionRef) string { return ref.Name + "@" + ref.Version + "|" + ref.Image }

func (r *DockerRunner) Invoke(ctx context.Context, ref FunctionRef, invokeRequestJSON []byte) (*InvokeResponse, error) {
	ref.Timeout = ClampTimeout(ref.Timeout)
	ctx, cancel := context.WithTimeout(ctx, ref.Timeout+15*time.Second) // cold start budget on top
	defer cancel()

	if err := r.ensureImage(ctx, ref.Image); err != nil {
		return nil, err
	}
	inst, err := r.ensureInstance(ctx, ref)
	if err != nil {
		return nil, err
	}
	resp, err := r.postInvoke(ctx, inst, invokeRequestJSON)
	if err != nil {
		// One retry: container may have died between healthz and invoke.
		slog.Warn("invoke failed, cold-starting once more", "fn", ref.Name, "err", err)
		r.removeInstance(ref, inst)
		inst, err = r.ensureInstance(ctx, ref)
		if err != nil {
			return nil, err
		}
		resp, err = r.postInvoke(ctx, inst, invokeRequestJSON)
		if err != nil {
			return nil, err
		}
	}
	r.touch(ref, inst)
	return resp, nil
}

// ensureImage reloads a missing image from the persisted tarball
// (daemon restart / image prune survival).
func (r *DockerRunner) ensureImage(ctx context.Context, image string) error {
	if r.cfg.Arts == nil {
		return nil
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(inspectCtx, "docker", "image", "inspect", image).Run(); err == nil {
		return nil
	}
	slog.Info("image missing locally, reloading from artifacts", "image", image)
	tar, err := r.cfg.Arts.Get(ctx, artifacts.ImageKey(image))
	if err != nil {
		return fmt.Errorf("image %s missing and no persisted copy: %v", image, err)
	}
	tmp, err := os.CreateTemp("", "actions-image-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(tar); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "docker", "load", "-i", tmp.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker load: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *DockerRunner) postInvoke(ctx context.Context, inst *instance, body []byte) (*InvokeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", inst.base+"/invoke", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		slurp, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64*1024))
		return nil, fmt.Errorf("handler /invoke returned %d: %s", httpResp.StatusCode, strings.TrimSpace(string(slurp)))
	}
	var out InvokeResponse
	if err := json.NewDecoder(io.LimitReader(httpResp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode InvokeResponse: %w", err)
	}
	if out.Headers == nil {
		out.Headers = map[string]string{}
	}
	return &out, nil
}

// ensureInstance returns a warm container or cold-starts one (single-flight).
func (r *DockerRunner) ensureInstance(ctx context.Context, ref FunctionRef) (*instance, error) {
	k := keyFor(ref)
	for {
		r.mu.Lock()
		if inst, ok := r.instances[k]; ok {
			r.mu.Unlock()
			if r.alive(ctx, inst) {
				return inst, nil
			}
			r.removeInstance(ref, inst)
			continue
		}
		if ch, ok := r.starting[k]; ok {
			r.mu.Unlock()
			select {
			case <-ch:
				continue // starter finished; loop to pick up instance (or start ourselves)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		r.starting[k] = ch
		r.mu.Unlock()

		inst, err := r.coldStart(ctx, ref)
		r.mu.Lock()
		if err == nil {
			r.instances[k] = inst
		}
		close(ch)
		delete(r.starting, k)
		r.mu.Unlock()
		return inst, err
	}
}

func (r *DockerRunner) alive(ctx context.Context, inst *instance) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", inst.base+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (r *DockerRunner) touch(ref FunctionRef, inst *instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.instances[keyFor(ref)]; ok && cur == inst {
		cur.lastUsed = time.Now()
	}
}

func (r *DockerRunner) removeInstance(ref FunctionRef, inst *instance) {
	r.mu.Lock()
	if cur, ok := r.instances[keyFor(ref)]; ok && cur == inst {
		delete(r.instances, keyFor(ref))
	}
	r.mu.Unlock()
	if r.cfg.Tokens != nil && inst.token != "" {
		r.cfg.Tokens.Revoke(ref.Name, inst.token)
	}
	_ = r.dockerKill(inst.containerID)
}

// isolated reports whether this function must run on the sandbox network.
func (r *DockerRunner) isolated(ref FunctionRef) bool {
	return r.cfg.SandboxNet != "" && !ref.AllowEgress
}

func (r *DockerRunner) coldStart(ctx context.Context, ref FunctionRef) (*instance, error) {
	name := "actions-" + sanitize(ref.Name) + "-" + randHex(6)
	var token string
	if r.cfg.Tokens != nil {
		token = r.cfg.Tokens.Mint(ref.Name)
	}
	isolated := r.isolated(ref)
	network := r.cfg.Network
	var hostPort string
	if isolated {
		if err := r.ensureSandboxNet(ctx); err != nil {
			return nil, err
		}
		network = r.cfg.SandboxNet
	} else {
		var err error
		hostPort, err = freePort()
		if err != nil {
			return nil, err
		}
		slog.Warn("egress deny not enforced on shared network (dev mode), logging only", "fn", ref.Name)
	}
	mem := ref.MemoryMB
	if mem <= 0 {
		mem = DefaultMemory
	}
	args := buildRunArgs(ref, name, hostPort, network, mem, ref.SidecarURL, token, r.probeRunsc(ctx))
	id, err := r.dockerRun(ctx, args)
	if err != nil {
		if r.shouldFallbackToDefaultRuntime(err) {
			slog.Warn("runsc runtime unavailable, falling back to default runtime", "fn", ref.Name)
			f := false
			r.useRunsc = &f
			args = buildRunArgs(ref, name, hostPort, network, mem, ref.SidecarURL, token, false)
			id, err = r.dockerRun(ctx, args)
		}
		if err != nil {
			return nil, err
		}
	}
	base := "http://" + r.cfg.HostGW + ":" + hostPort
	if isolated {
		base = "http://" + name + ":" + containerPort
	}
	inst := &instance{containerID: id, fn: ref.Name, name: name, base: base, token: token, lastUsed: time.Now()}
	if err := r.waitHealthy(ctx, inst); err != nil {
		r.removeInstance(ref, inst)
		return nil, fmt.Errorf("cold start %s: %w", ref.Image, err)
	}
	slog.Info("cold start ok", "fn", ref.Name, "container", shortID(id), "isolated", isolated)
	return inst, nil
}

// buildRunArgs assembles the sandboxed `docker run` argv (pure, unit-tested).
// Empty hostPort = no published ports (isolated sandbox mode).
func buildRunArgs(ref FunctionRef, cname, hostPort, network string, memMB int, sidecarURL, token string, useRunsc bool) []string {
	args := []string{"run", "-d",
		"--name", cname,
		"--label", "actions-managed=true",
		"--label", "actions-fn=" + sanitize(ref.Name),
		"--read-only",
		"--tmpfs", "/tmp:rw,size=64m",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--memory", fmt.Sprintf("%dm", memMB),
		"--memory-swap", fmt.Sprintf("%dm", memMB),
		"--cpus", "0.5",
		"--pids-limit", "128",
		"--ulimit", "nofile=256:256",
	}
	if useRunsc {
		args = append(args, "--runtime=runsc")
	}
	args = append(args, "--network", network)
	if hostPort != "" {
		args = append(args, "-p", "127.0.0.1:"+hostPort+":"+containerPort)
	}
	args = append(args,
		"-e", "PORT="+containerPort,
		"-e", "ACTIONS_SIDECAR_URL="+sidecarURL,
	)
	if token != "" {
		args = append(args, "-e", "ACTIONS_SIDECAR_TOKEN="+token)
	}
	return append(args, ref.Image)
}

func (r *DockerRunner) ensureSandboxNet(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "network", "create", "--internal", r.cfg.SandboxNet).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			return nil
		}
		return fmt.Errorf("create sandbox net: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *DockerRunner) waitHealthy(ctx context.Context, inst *instance) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", inst.base+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := r.client.Do(req)
		if err == nil {
			ok := resp.StatusCode == http.StatusOK
			resp.Body.Close()
			if ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("healthz never turned 200 at %s", inst.base)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *DockerRunner) reapLoop() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			now := time.Now()
			var stale []*instance
			var keys []string
			r.mu.Lock()
			for k, inst := range r.instances {
				if now.Sub(inst.lastUsed) > IdleTTL {
					stale = append(stale, inst)
					keys = append(keys, k)
				}
			}
			for _, k := range keys {
				delete(r.instances, k)
			}
			r.mu.Unlock()
			for _, inst := range stale {
				slog.Info("idle TTL expired, stopping", "container", shortID(inst.containerID))
				if r.cfg.Tokens != nil && inst.token != "" {
					r.cfg.Tokens.Revoke(inst.fn, inst.token)
				}
				_ = r.dockerKill(inst.containerID)
			}
		case <-r.reapStop:
			return
		}
	}
}

func (r *DockerRunner) Close() {
	close(r.reapStop)
	r.mu.Lock()
	insts := make([]*instance, 0, len(r.instances))
	for _, inst := range r.instances {
		insts = append(insts, inst)
	}
	r.instances = map[string]*instance{}
	r.mu.Unlock()
	for _, inst := range insts {
		_ = r.dockerKill(inst.containerID)
	}
}

// --- docker CLI plumbing ---

func (r *DockerRunner) dockerRun(ctx context.Context, args []string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *DockerRunner) probeRunsc(ctx context.Context) bool {
	if r.useRunsc != nil {
		return *r.useRunsc
	}
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.Runtimes}}").CombinedOutput()
	use := err == nil && strings.Contains(string(out), "runsc")
	if err != nil {
		use = false
	}
	r.useRunsc = &use
	return use
}

func (r *DockerRunner) shouldFallbackToDefaultRuntime(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unknown runtime") || strings.Contains(s, "invalid runtime")
}

func (r *DockerRunner) dockerKill(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rm -f: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return "", err
	}
	return port, nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "fn"
	}
	return b.String()
}

func randHex(n int) string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])[:n]
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
