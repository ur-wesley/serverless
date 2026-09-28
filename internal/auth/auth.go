// Package auth provides operator credentials, opaque bearer tokens,
// per-function API keys, device-code login helpers and public slugs.
// Secrets are never stored in cleartext server-side: passwords use
// bcrypt, tokens/keys use SHA-256 hashes. Full secrets are shown once
// at creation and then unrecoverable.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Auth modes for functions (actions.toml `auth_mode`, DB `auth_mode`).
const (
	ModePublic  = "public"  // open invoke (default, backwards compatible)
	ModeKey     = "key"     // requires x-api-key / ?key=
	ModePrivate = "private" // requires operator Bearer token
)

// Token / session lifetimes.
const (
	SessionTTL    = 30 * 24 * time.Hour
	DeviceTTL     = 10 * time.Minute
	DevicePollSec = 5
)

// NormalizeMode lowercases s and defaults empty/unknown to public.
// Unknown values are rejected by ValidateMode; this is for reads.
func NormalizeMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ModeKey:
		return ModeKey
	case ModePrivate:
		return ModePrivate
	default:
		return ModePublic
	}
}

// ValidateMode reports whether s is a supported auth mode.
func ValidateMode(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", ModePublic, ModeKey, ModePrivate:
		return nil
	}
	return fmt.Errorf("unsupported auth_mode %q (want public|key|private)", s)
}

// ValidateUsername enforces a safe namespace: 2-32 chars, [a-z0-9-_],
// lowercase. Used as URL namespace /f/<user>/<name>.
func ValidateUsername(s string) error {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 2 || len(s) > 32 {
		return fmt.Errorf("username must be 2..32 chars")
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("username must match [a-z0-9-_]")
	}
	return nil
}

// ValidateFunctionName enforces function names safe for URLs.
func ValidateFunctionName(s string) error {
	if len(s) < 1 || len(s) > 64 {
		return fmt.Errorf("function name must be 1..64 chars")
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("function name must match [A-Za-z0-9-_]")
	}
	return nil
}

// HashPassword hashes with bcrypt (cost 11).
func HashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", fmt.Errorf("password must be at least 8 chars")
	}
	if len(pw) > 128 {
		return "", fmt.Errorf("password too long")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 11)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword compares bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// HashSecret returns hex(sha256(secret)) for token/key storage.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CheckSecret compares a stored hash with a presented secret.
func CheckSecret(storedHash, secret string) bool {
	got := HashSecret(secret)
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

func randHex(n int) (string, error) {
	var b = make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewOperatorToken mints `act_<64 hex>`; returns full secret + sha256 hash.
func NewOperatorToken() (full, hash string, err error) {
	hexPart, err := randHex(32)
	if err != nil {
		return "", "", err
	}
	full = "act_" + hexPart
	return full, HashSecret(full), nil
}

// NewAPIKey mints `ak_<prefix>_<secret>`; prefix is 4 chars for listing.
// Returns full secret, prefix, sha256 hash.
func NewAPIKey() (full, prefix, hash string, err error) {
	p, err := randHex(2) // 4 hex chars
	if err != nil {
		return "", "", "", err
	}
	s, err := randHex(24) // 48 hex chars
	if err != nil {
		return "", "", "", err
	}
	full = "ak_" + p + "_" + s
	return full, p, HashSecret(full), nil
}

// NewDeviceCode mints an opaque device_code (hex, never shown to user).
func NewDeviceCode() (string, error) { return randHex(32) }

// NewUserCode mints `XXXX-XXXX` from unambiguous alphabet (no 0/O/1/I).
func NewUserCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	var b [8]byte
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i, v := range raw {
		b[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:]), nil
}

// NewSlug mints an 8-char public slug for /s/<slug> URLs.
func NewSlug() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b [8]byte
	for i, v := range raw {
		b[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(b[:]), nil
}

// Bearer strips "Bearer " prefix.
func Bearer(h string) string {
	if len(h) > 7 && (h[:7] == "Bearer " || h[:7] == "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
