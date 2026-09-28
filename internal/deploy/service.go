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

	"actions/internal/artifacts"
	"actions/internal/auth"
	"actions/internal/store"
)

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

// BuildFunc compiles srcDir into outImage. ExtractFunc returns /app/handler
// bytes from a built image. Docker-backed defaults; fakes in tests.
type BuildFunc func(ctx context.Context, srcDir, runtime, outImage string) error
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
	case "ts", "go", "rust", "zig":
	default:
		return fmt.Errorf("unsupported runtime %q (want ts|go|rust|zig)", c.Runtime)
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
	return nil
}

func (s *Service) Deploy(ctx context.Context, req Request) (*Result, error) {
	cfg, err := ParseConfig(req.ConfigTOML)
	if err != nil {
		return nil, err
	}
	if err := Validate(req.Name, cfg); err != nil {
		return nil, err
	}
	m := s.lockFor(req.Name)
	m.Lock()
	defer m.Unlock()
	return s.deployLocked(ctx, req, cfg)
}

func (s *Service) deployLocked(ctx context.Context, req Request, cfg Config) (*Result, error) {
	if len(req.SrcZip) == 0 {
		return nil, fmt.Errorf("src.zip empty")
	}
	srcDir, err := unzipToTemp(req.SrcZip)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(srcDir)
	if err := checkRuntimeLayout(cfg.Runtime, srcDir); err != nil {
		return nil, err
	}

	ver, err := s.Store.NextVersion(req.Name)
	if err != nil {
		return nil, err
	}
	if err := s.Artifacts.Put(ctx, artifacts.BundleKey(req.Name, ver, "src.zip"), req.SrcZip); err != nil {
		return nil, fmt.Errorf("store src.zip: %w", err)
	}

	image := "actions/" + strings.ToLower(req.Name) + ":" + ver
	build := s.Build
	if build == nil {
		build = s.dockerBuild
	}
	if err := build(ctx, srcDir, cfg.Runtime, image); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	extract := s.Extract
	if extract == nil {
		extract = dockerExtractHandler
	}
	handler, err := extract(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("extract handler: %w", err)
	}
	sum := sha256.Sum256(handler)
	sha := hex.EncodeToString(sum[:])
	if err := s.Artifacts.Put(ctx, artifacts.BundleKey(req.Name, ver, "handler"), handler); err != nil {
		return nil, fmt.Errorf("store handler: %w", err)
	}
	if err := s.Artifacts.Put(ctx, artifacts.BundleKey(req.Name, ver, "sha256"), []byte(sha)); err != nil {
		return nil, fmt.Errorf("store sha256: %w", err)
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
		Name: req.Name, Ver: ver, SHA256: sha,
		HandlerKey: artifacts.BundleKey(req.Name, ver, "handler"),
		ConfigJSON: "{}",
		Status:     "active",
		OwnerID:    req.OwnerID,
	}); err != nil {
		return nil, err
	}
	mode := auth.NormalizeMode(cfg.AuthMode)
	slug := ""
	if existing, err := s.Store.GetFunction(req.Name); err == nil {
		slug = existing.Slug
	}
	if slug == "" {
		for i := 0; i < 5; i++ {
			cand, err := auth.NewSlug()
			if err != nil {
				return nil, err
			}
			if !s.Store.SlugExists(cand) {
				slug = cand
				break
			}
		}
		if slug == "" {
			return nil, fmt.Errorf("slug allocation failed")
		}
	}
	if err := s.Store.UpsertFunctionOwned(req.OwnerID, req.Name, ver, req.ConfigTOML, slug, mode); err != nil {
		return nil, err
	}
	return &Result{Name: req.Name, Version: ver, Status: "active", Image: image, SHA256: sha}, nil
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

func (s *Service) dockerBuild(ctx context.Context, srcDir, runtime, outImage string) error {
	df := filepath.Join(s.buildersDir(), "builder-"+runtime, "Dockerfile")
	if _, err := os.Stat(df); err != nil {
		return fmt.Errorf("no builder for runtime %q (%s)", runtime, df)
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "-f", df, "-t", outImage, srcDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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
