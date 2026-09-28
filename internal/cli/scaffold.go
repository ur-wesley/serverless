// Package cli implements the `actions` subcommands (init/dev/deploy/invoke/logs/ls).
package cli

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/ts/* templates/ts/src/* templates/go/*
var templatesFS embed.FS

type templateData struct{ Name string }

// Init scaffolds actions.toml + src for ts|go into dir (created if missing).
func Init(runtime, name, dir string) error {
	if runtime != "ts" && runtime != "go" {
		return fmt.Errorf("unsupported runtime %q (want ts|go)", runtime)
	}
	if name == "" {
		return fmt.Errorf("--name required")
	}
	var srcs []string
	switch runtime {
	case "ts":
		srcs = []string{"templates/ts/actions.toml.tmpl", "templates/ts/package.json.tmpl", "templates/ts/src/index.ts.tmpl"}
	case "go":
		srcs = []string{"templates/go/actions.toml.tmpl", "templates/go/go.mod.tmpl", "templates/go/main.go.tmpl"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, src := range srcs {
		raw, err := templatesFS.ReadFile(src)
		if err != nil {
			return err
		}
		t, err := template.New("f").Parse(string(raw))
		if err != nil {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(src, "templates/"+runtime+"/"), ".tmpl")
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("refusing to overwrite %s", dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		err = t.Execute(f, templateData{Name: name})
		cerr := f.Close()
		if err != nil {
			return err
		}
		if cerr != nil {
			return cerr
		}
	}
	return nil
}
