package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"actions/internal/artifacts"
	"actions/internal/store"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{
		Store:     st,
		Artifacts: artifacts.NewLocal(t.TempDir()),
		Build: func(_ context.Context, _, _, _ string) error {
			return nil // fake: skip docker
		},
		Extract: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("fake-handler-binary"), nil
		},
	}
}

const goodTOML = `
name = "hello"
runtime = "go"
route = "/f/hello"
timeout_ms = 10000
memory_mb = 256
allow_egress = false
`

func TestDeployHappyPath(t *testing.T) {
	s := testService(t)
	src := makeZip(t, map[string]string{"go.mod": "module hello\n\ngo 1.27\n", "main.go": "package main\n"})
	res, err := s.Deploy(context.Background(), Request{Name: "hello", ConfigTOML: goodTOML, SrcZip: src})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "v1" || res.Status != "active" {
		t.Fatalf("res = %+v", res)
	}
	if res.Image != "actions/hello:v1" {
		t.Fatalf("image = %q", res.Image)
	}
	if len(res.SHA256) != 64 {
		t.Fatalf("sha = %q", res.SHA256)
	}
	got, err := s.Artifacts.Get(context.Background(), "bundles/hello/v1/src.zip")
	if err != nil || len(got) == 0 {
		t.Fatalf("src.zip not stored: %v", err)
	}
	fn, err := s.Store.GetFunction("hello")
	if err != nil || fn.ActiveVersion != "v1" {
		t.Fatalf("fn = %+v, err = %v", fn, err)
	}
	// Second deploy bumps the version.
	res2, err := s.Deploy(context.Background(), Request{Name: "hello", ConfigTOML: goodTOML, SrcZip: src})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Version != "v2" {
		t.Fatalf("second ver = %q, want v2", res2.Version)
	}
}

func TestZipCaps(t *testing.T) {
	oldFiles, oldTotal, oldFile := MaxZipFiles, MaxZipTotal, MaxZipFile
	MaxZipFiles, MaxZipTotal, MaxZipFile = 2, 100, 60
	defer func() { MaxZipFiles, MaxZipTotal, MaxZipFile = oldFiles, oldTotal, oldFile }()

	s := testService(t)
	if _, err := s.Deploy(context.Background(), Request{
		Name: "hello", ConfigTOML: goodTOML,
		SrcZip: makeZip(t, map[string]string{"go.mod": "module hello\n", "a": "x", "b": "y"}),
	}); err == nil || !strings.Contains(err.Error(), "max 2") {
		t.Fatalf("file count: %v", err)
	}
	big := strings.Repeat("x", 80)
	if _, err := s.Deploy(context.Background(), Request{
		Name: "hello", ConfigTOML: goodTOML,
		SrcZip: makeZip(t, map[string]string{"go.mod": "module hello\n", "big": big}),
	}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("per-file cap: %v", err)
	}
}

func TestCronTableHint(t *testing.T) {
	_, err := ParseConfig("name=\"x\"\nruntime=\"go\"\n[[cron]] = \"*/5 * * * *\"\n")
	if err == nil || !strings.Contains(err.Error(), "cron = [") {
		t.Fatalf("hint: %v", err)
	}
}

func TestDeployValidation(t *testing.T) {
	s := testService(t)
	src := makeZip(t, map[string]string{"go.mod": "module hello\n"})
	cases := map[string]struct {
		name string
		toml string
		zip  map[string]string
	}{
		"name mismatch":     {"other", goodTOML, nil},
		"bad runtime":       {"hello", "name=\"hello\"\nruntime=\"py\"\n", nil},
		"bad timeout":       {"hello", "name=\"hello\"\nruntime=\"go\"\ntimeout_ms=5\n", nil},
		"empty zip":         {"hello", goodTOML, map[string]string{}},
		"missing go.mod":    {"hello", goodTOML, map[string]string{"main.go": "x"}},
		"unparseable toml":  {"hello", "name = [oops", nil},
	}
	_ = src
	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			z := src
			if tc.zip != nil {
				z = makeZip(t, tc.zip)
			}
			if _, err := s.Deploy(context.Background(), Request{Name: tc.name, ConfigTOML: tc.toml, SrcZip: z}); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
