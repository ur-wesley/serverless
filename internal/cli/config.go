package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// Auth is the persisted CLI login: control plane URL + operator token.
// Stored with 0600 permissions; secrets never go into actions.toml.
type Auth struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Username string `json:"username"`
}

func authFilePath() string {
	if v := os.Getenv("ACTIONS_AUTH_FILE"); v != "" {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "actions", "auth.json")
}

// LoadAuth reads the saved login, or zero Auth when logged out.
func LoadAuth() (Auth, error) {
	var a Auth
	raw, err := os.ReadFile(authFilePath())
	if err != nil {
		return Auth{}, err
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return Auth{}, err
	}
	return a, nil
}

// SaveAuth persists URL + token with 0600 permissions.
func SaveAuth(a Auth) error {
	p := authFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(a, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ClearAuth removes the saved login (logout).
func ClearAuth() error {
	err := os.Remove(authFilePath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ResolveURL picks flag > env > saved > default.
func ResolveURL(flag string) string {
	if flag != "" {
		return strings.TrimSuffix(flag, "/")
	}
	if v := os.Getenv("ACTIONS_URL"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if a, err := LoadAuth(); err == nil && a.URL != "" {
		return strings.TrimSuffix(a.URL, "/")
	}
	return "http://localhost:8080"
}

// ResolveToken picks flag > env > saved login.
func ResolveToken(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("ACTIONS_TOKEN"); v != "" {
		return v
	}
	if a, err := LoadAuth(); err == nil {
		return a.Token
	}
	return ""
}

// Prompt reads a line from stdin.
func Prompt(msg string) (string, error) {
	fmt.Print(msg)
	rd := bufio.NewReader(os.Stdin)
	s, err := rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// PromptPassword reads without echo when stdin is a terminal.
func PromptPassword(msg string) (string, error) {
	fmt.Print(msg)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		rd := bufio.NewReader(os.Stdin)
		s, err := rd.ReadString('\n')
		fmt.Println()
		return strings.TrimSpace(s), err
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
