// Package deploy implements POST /deploy:
// validate actions.toml -> RustFS bundles/<name>/<ver>/src.zip ->
// builder-<runtime> compiles -> image actions/<name>:<ver> (+ handler+sha256
// in artifacts) -> SQLite version=active. The runner pulls the image on the
// next trigger.
package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/robfig/cron/v3"

	"actions/internal/artifacts"
	"actions/internal/auth"
	"actions/internal/store"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type Config struct {
	Name        string   `toml:"name"`
	Runtime     string   `toml:"runtime"`
	Route       string   `toml:"route"`
	TimeoutMs   int      `toml:"timeout_ms"`
	MemoryMB    int      `toml:"memory_mb"`
	AllowEgress bool     `toml:"allow_egress"`
	AuthMode    string   `toml:"auth_mode"`
	Cron        []string `toml:"cron"`
	Queue       []string `toml:"queue"`
	// Version pins this deploy ("1.2.3"); Bump auto-increments
	// ("major"|"minor"|"patch", default patch). Mutually exclusive.
	// Absent both, the server bumps patch. First deploy starts at 0.1.0.
	Version string `toml:"version"`
	Bump    string `toml:"bump"`
	// Intercom: opt-in cross-function access, same-owner only.
	// "*" = all own functions. Empty = self only (default deny).
	AllowLogs   []string `toml:"allow_logs"`
	AllowInvoke []string `toml:"allow_invoke"`
	AllowKV     []string `toml:"allow_kv"`
	AllowBlobs  []string `toml:"allow_blobs"`
}

type Request struct {
	Name       string
	ConfigTOML string
	SrcZip     []byte
	OwnerID    string
}

type Result struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Image   string `json:"image"`
	SHA256  string `json:"sha256"`
}

// BuildFunc compiles srcDir into outImage, returning the combined build log.
// ExtractFunc returns /app/handler bytes from a built image.
// Docker-backed defaults; fakes in tests.
type BuildFunc func(ctx context.Context, srcDir, runtime, outImage string) (string, error)
type ExtractFunc func(ctx context.Context, outImage string) ([]byte, error)

type Service struct {
	Store       *store.Store
	Artifacts   artifacts.Store
	Build       BuildFunc
	Extract     ExtractFunc
	BuildersDir string // dir containing builder-<runtime>/Dockerfile (default "builders")

	mu       sync.Mutex
	deployMu map[string]*sync.Mutex // serializes deploys per function (version ordering)
}

// Zip bomb guardrails (vars for tests).
var (
	MaxZipFiles = 2000
	MaxZipTotal = int64(256 << 20)
	MaxZipFile  = int64(64 << 20)
)

func (s *Service) lockFor(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deployMu == nil {
		s.deployMu = map[string]*sync.Mutex{}
	}
	m, ok := s.deployMu[name]
	if !ok {
		m = &sync.Mutex{}
		s.deployMu[name] = m
	}
	return m
}

func (s *Service) buildersDir() string {
	if s.BuildersDir != "" {
		return s.BuildersDir
	}
	return "builders"
}

func ParseConfig(tomlText string) (Config, error) {
	var c Config
	if _, err := toml.Decode(tomlText, &c); err != nil {
		if strings.Contains(tomlText, "[[cron]]") || strings.Contains(tomlText, "[[queue]]") {
			return Config{}, fmt.Errorf("parse actions.toml: %v (hint: use arrays — cron = [\"*/5 * * * *\"], queue = [\"topic\"])", err)
		}
		return Config{}, fmt.Errorf("parse actions.toml: %w", err)
	}
	return c, nil
}

func Validate(name string, c Config) error {
	if c.Name == "" {
		return fmt.Errorf("name required")
	}
	if c.Name != name {
		return fmt.Errorf("config name %q != request name %q", c.Name, name)
	}
	switch c.Runtime {
	case "ts", "go":
	default:
		return fmt.Errorf("unsupported runtime %q (want ts|go — rust|zig builders not shipped yet)", c.Runtime)
	}
	if c.TimeoutMs != 0 && (c.TimeoutMs < 1000 || c.TimeoutMs > 60000) {
		return fmt.Errorf("timeout_ms %d out of range 1000..60000", c.TimeoutMs)
	}
	if c.MemoryMB != 0 && (c.MemoryMB < 64 || c.MemoryMB > 2048) {
		return fmt.Errorf("memory_mb %d out of range 64..2048", c.MemoryMB)
	}
	if c.Route != "" && !strings.HasPrefix(c.Route, "/") {
		return fmt.Errorf("route %q must start with /", c.Route)
	}
	switch strings.ToLower(strings.TrimSpace(c.AuthMode)) {
	case "", "public", "key", "private":
	default:
		return fmt.Errorf("auth_mode %q must be public|key|private", c.AuthMode)
	}
	if c.Version != "" && c.Bump != "" {
		return fmt.Errorf("version and bump are mutually exclusive")
	}
	switch strings.ToLower(strings.TrimSpace(c.Bump)) {
	case "", "major", "minor", "patch":
	default:
		return fmt.Errorf("bump %q must be major|minor|patch", c.Bump)
	}
	if c.Version != "" {
		if _, err := ParseVersion(c.Version); err != nil {
			return err
		}
	}
	for _, spec := range c.Cron {
		if _, err := cronParser.Parse(spec); err != nil {
			return fmt.Errorf("cron %q invalid: %v", spec, err)
		}
	}
	for _, t := range c.Queue {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("queue topic must not be blank")
		}
	}
	for _, entry := range [][]string{c.AllowLogs, c.AllowInvoke, c.AllowKV, c.AllowBlobs} {
		for _, name := range entry {
			if name == "" || name == "*" {
				continue
			}
			if err := auth.ValidateFunctionName(name); err != nil {
				return fmt.Errorf("allow list entry %q: %w", name, err)
			}
		}
	}
	return nil
}

func (s *Service) Deploy(ctx context.Context, req Request) (*Result, error) {
	job, err := s.Enqueue(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.Execute(ctx, *job)
}

// Enqueue validates the deploy, reserves a semver version, stores src.zip
// and creates a queued job. Fast: no docker calls.
func (s *Service) Enqueue(ctx context.Context, req Request) (*store.Job, error) {
	cfg, err := ParseConfig(req.ConfigTOML)
	if err != nil {
		return nil, err
	}
	if err := Validate(req.Name, cfg); err != nil {
		return nil, err
	}
	if len(req.SrcZip) == 0 {
		return nil, fmt.Errorf("src.zip empty")
	}
	m := s.lockFor(req.Name)
	m.Lock()
	defer m.Unlock()
	ver, err := s.ResolveVersion(req.Name, cfg)
	if err != nil {
		return nil, err
	}
	if err := s.Artifacts.Put(ctx, artifacts.BundleKey(req.Name, ver, "src.zip"), req.SrcZip); err != nil {
		return nil, fmt.Errorf("store src.zip: %w", err)
	}
	job, err := s.Store.CreateJob(req.OwnerID, req.Name, req.ConfigTOML, ver)
	if err != nil {
		return nil, err
	}
	s.appendLog(ctx, job, fmt.Sprintf("queued %s %s", req.Name, ver))
	return &job, nil
}

// Execute runs a queued job to completion (build + activate), recording the
// build log and the terminal job status. Safe to call from the Worker or
// directly for the synchronous path.
func (s *Service) Execute(ctx context.Context, job store.Job) (*Result, error) {
	m := s.lockFor(job.FnName)
	m.Lock()
	defer m.Unlock()
	return s.executeLocked(ctx, job)
}

func (s *Service) fail(ctx context.Context, job store.Job, err error) (*Result, error) {
	msg := err.Error()
	if len(msg) > 2048 {
		msg = msg[:2048]
	}
	s.appendLog(ctx, job, "FAILED: "+msg)
	_ = s.Store.UpdateJobStatus(job.ID, store.JobFailed, job.Version, "", "", msg)
	return nil, err
}

// ResolveVersion decides the version string for a deploy: explicit `version`
// (must exceed active), or `bump` applied to active (first deploy: 0.1.0).
// Callers must hold the per-function deploy lock: the uniqueness check and
// the later InsertVersion are only ordered under it.
func (s *Service) ResolveVersion(name string, cfg Config) (string, error) {
	active := ""
	if fn, err := s.Store.GetFunction(name); err == nil {
		active = fn.ActiveVersion
	}
	if cfg.Version != "" {
		req, err := ParseVersion(cfg.Version)
		if err != nil {
			return "", err
		}
		if active != "" {
			cur, err := ParseVersion(active)
			if err != nil {
				return "", fmt.Errorf("active version %q unparseable: %w", active, err)
			}
			if Compare(req, cur) <= 0 {
				return "", fmt.Errorf("version %s must exceed active %s", req.Raw, cur.Raw)
			}
		}
		if _, err := s.Store.GetVersion(name, req.Raw); err == nil {
			return "", fmt.Errorf("version %s already exists", req.Raw)
		}
		return req.Raw, nil
	}
	if active == "" {
		return "0.1.0", nil
	}
	cur, err := ParseVersion(active)
	if err != nil {
		return "", fmt.Errorf("active version %q unparseable: %w", active, err)
	}
	level := strings.ToLower(strings.TrimSpace(cfg.Bump))
	if level == "" {
		level = "patch"
	}
	next := cur.Bump(level)
	if _, err := s.Store.GetVersion(name, next.Raw); err == nil {
		return "", fmt.Errorf("version %s already exists", next.Raw)
	}
	return next.Raw, nil
}

func (s *Service) executeLocked(ctx context.Context, job store.Job) (*Result, error) {
	cfg, err := ParseConfig(job.ConfigTOML)
	if err != nil {
		return s.fail(ctx, job, err)
	}
	ver := job.Version
	srcZip, err := s.Artifacts.Get(ctx, artifacts.BundleKey(job.FnName, ver, "src.zip"))
	if err != nil || len(srcZip) == 0 {
		return s.fail(ctx, job, fmt.Errorf("src.zip missing for %s %s", job.FnName, ver))
	}
	srcDir, err := unzipToTemp(srcZip)
	if err != nil {
		return s.fail(ctx, job, err)
	}
	defer os.RemoveAll(srcDir)
	if err := checkRuntimeLayout(cfg.Runtime, srcDir); err != nil {
		return s.fail(ctx, job, err)
	}

	image := "actions/" + strings.ToLower(job.FnName) + ":" + DockerTagFor(ver)
	s.appendLog(ctx, job, fmt.Sprintf("building %s (%s) -> %s", job.FnName, cfg.Runtime, image))
	build := s.Build
	if build == nil {
		build = s.dockerBuild
	}
	out, err := build(ctx, srcDir, cfg.Runtime, image)
	if out != "" {
		s.appendLog(ctx, job, out)
	}
	if err != nil {
		return s.fail(ctx, job, fmt.Errorf("build: %w", err))
	}
	extract := s.Extract
	if extract == nil {
		extract = dockerExtractHandler
	}
	handler, err := extract(ctx, image)
	if err != nil {
		return s.fail(ctx, job, fmt.Errorf("extract handler: %w", err))
	}
	sum := sha256.Sum256(handler)
	sha := hex.EncodeToString(sum[:])
	if err := s.Artifacts.Put(ctx, artifacts.BundleKey(job.FnName, ver, "handler"), handler); err != nil {
		return s.fail(ctx, job, fmt.Errorf("store handler: %w", err))
	}
	if err := s.Artifacts.Put(ctx, artifacts.BundleKey(job.FnName, ver, "sha256"), []byte(sha)); err != nil {
		return s.fail(ctx, job, fmt.Errorf("store sha256: %w", err))
	}
	// Persist the image tarball so the runner survives daemon restarts.
	if s.Build == nil {
		if tar, err := dockerSaveImage(ctx, image); err != nil {
			slog.Warn("image persist failed (daemon restart will require rebuild)", "image", image, "err", err)
		} else if err := s.Artifacts.Put(ctx, artifacts.ImageKey(image), tar); err != nil {
			slog.Warn("image persist failed", "image", image, "err", err)
		}
	}
	if err := s.Store.InsertVersion(store.Version{
		Name: job.FnName, Ver: ver, SHA256: sha,
		HandlerKey: artifacts.BundleKey(job.FnName, ver, "handler"),
		ConfigJSON: "{}",
		Status:     "active",
		OwnerID:    job.OwnerID,
	}); err != nil {
		return s.fail(ctx, job, err)
	}
	mode := auth.NormalizeMode(cfg.AuthMode)
	slug := ""
	if existing, err := s.Store.GetFunction(job.FnName); err == nil {
		slug = existing.Slug
	}
	if slug == "" {
		for i := 0; i < 5; i++ {
			cand, err := auth.NewSlug()
			if err != nil {
				return s.fail(ctx, job, err)
			}
			if !s.Store.SlugExists(cand) {
				slug = cand
				break
			}
		}
		if slug == "" {
			return s.fail(ctx, job, fmt.Errorf("slug allocation failed"))
		}
	}
	if err := s.Store.UpsertFunctionOwned(job.OwnerID, job.FnName, ver, job.ConfigTOML, slug, mode); err != nil {
		return s.fail(ctx, job, err)
	}
	s.appendLog(ctx, job, fmt.Sprintf("active %s %s sha256=%.12s", job.FnName, ver, sha))
	_ = s.Store.UpdateJobStatus(job.ID, store.JobActive, ver, image, sha, "")
	return &Result{Name: job.FnName, Version: ver, Status: "active", Image: image, SHA256: sha}, nil
}

func unzipToTemp(zipBytes []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", fmt.Errorf("unzip: %w", err)
	}
	if len(zr.File) > MaxZipFiles {
		return "", fmt.Errorf("src.zip has %d files (max %d)", len(zr.File), MaxZipFiles)
	}
	dir, err := os.MkdirTemp("", "actions-src-*")
	if err != nil {
		return "", err
	}
	var total int64
	n := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > uint64(MaxZipFile) {
			os.RemoveAll(dir)
			return "", fmt.Errorf("src.zip entry %q too large (max %dMB)", f.Name, MaxZipFile>>20)
		}
		rc, err := f.Open()
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		// Zip-slip guard.
		dst := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(dir)) {
			rc.Close()
			os.RemoveAll(dir)
			return "", fmt.Errorf("zip-slip entry %q", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			rc.Close()
			os.RemoveAll(dir)
			return "", err
		}
		data := make([]byte, 0, min(f.UncompressedSize64, 1<<20))
		buf := make([]byte, 32*1024)
		for {
			m, rerr := rc.Read(buf)
			if m > 0 {
				total += int64(m)
				if total > MaxZipTotal {
					rc.Close()
					os.RemoveAll(dir)
					return "", fmt.Errorf("src.zip too large in total (max %dMB)", MaxZipTotal>>20)
				}
				data = append(data, buf[:m]...)
			}
			if rerr != nil {
				break
			}
		}
		rc.Close()
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		n++
	}
	if n == 0 {
		os.RemoveAll(dir)
		return "", fmt.Errorf("src.zip empty")
	}
	return dir, nil
}

func checkRuntimeLayout(runtime, dir string) error {
	has := func(p string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		return err == nil
	}
	switch runtime {
	case "ts":
		if !has("src/index.ts") && !has("package.json") {
			return fmt.Errorf("ts src needs src/index.ts or package.json")
		}
	case "go":
		if !has("go.mod") {
			return fmt.Errorf("go src needs go.mod")
		}
	case "rust":
		if !has("Cargo.toml") {
			return fmt.Errorf("rust src needs Cargo.toml")
		}
	case "zig":
		if !has("build.zig") {
			return fmt.Errorf("zig src needs build.zig")
		}
	}
	return nil
}

func (s *Service) dockerBuild(ctx context.Context, srcDir, runtime, outImage string) (string, error) {
	df := filepath.Join(s.buildersDir(), "builder-"+runtime, "Dockerfile")
	if _, err := os.Stat(df); err != nil {
		return "", fmt.Errorf("no builder for runtime %q (%s)", runtime, df)
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "-f", df, "-t", outImage, srcDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker build: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func dockerExtractHandler(ctx context.Context, outImage string) ([]byte, error) {
	cidOut, err := exec.CommandContext(ctx, "docker", "create", outImage).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker create: %v: %s", err, strings.TrimSpace(string(cidOut)))
	}
	cid := strings.TrimSpace(string(cidOut))
	defer exec.CommandContext(context.Background(), "docker", "rm", "-f", cid).CombinedOutput()
	tmp, err := os.MkdirTemp("", "actions-handler-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if out, err := exec.CommandContext(ctx, "docker", "cp", cid+":/app/handler", filepath.Join(tmp, "handler")).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker cp: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(filepath.Join(tmp, "handler"))
}

// dockerSaveImage persists the built image for runner reloads.
func dockerSaveImage(ctx context.Context, outImage string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "actions-image-*.tar")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	out, err := exec.CommandContext(ctx, "docker", "save", "-o", tmpName, outImage).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker save: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(tmpName)
}
