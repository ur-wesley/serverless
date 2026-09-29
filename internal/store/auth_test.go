package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ur-wesley/oort/internal/auth"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUsersSessions(t *testing.T) {
	s := openTest(t)
	if s.CountUsers() != 0 {
		t.Fatal("want zero users")
	}
	h, _ := auth.HashPassword("password123")
	u, err := s.CreateUser("Alice", h)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "alice" {
		t.Fatalf("name = %q, want lowercase alice", u.Name)
	}
	if _, err := s.CreateUser("alice", h); err == nil {
		t.Fatal("expected duplicate username error")
	}
	got, err := s.CheckUserPassword("ALICE", "password123", auth.CheckPassword)
	if err != nil || got.ID != u.ID {
		t.Fatalf("login failed: %v", err)
	}
	if _, err := s.CheckUserPassword("alice", "wrong", auth.CheckPassword); err == nil {
		t.Fatal("expected bad password error")
	}
	full, hash, _ := auth.NewOperatorToken()
	if err := s.CreateSession(hash, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if su, ok := s.GetSessionUser(hash); !ok || su.ID != u.ID {
		t.Fatal("session lookup failed")
	}
	// Expired sessions are rejected.
	expFull, expHash, _ := auth.NewOperatorToken()
	_ = expFull
	_ = s.CreateSession(expHash, u.ID, time.Now().Add(-time.Hour))
	if _, ok := s.GetSessionUser(expHash); ok {
		t.Fatal("expired session accepted")
	}
	s.DeleteSession(hash)
	if _, ok := s.GetSessionUser(hash); ok {
		t.Fatal("deleted session accepted")
	}
	_ = full
}

func TestDeviceFlow(t *testing.T) {
	s := openTest(t)
	dc, _ := auth.NewDeviceCode()
	uc, _ := auth.NewUserCode()
	if err := s.CreateDevice(auth.HashSecret(dc), uc, "cli@test", time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetDeviceByUserCode(uc)
	if err != nil || d.Status != "pending" {
		t.Fatalf("device = %+v, err = %v", d, err)
	}
	h, _ := auth.HashPassword("password123")
	u, _ := s.CreateUser("bob", h)
	if err := s.ApproveDevice(uc, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	d2, _ := s.GetDeviceByHash(auth.HashSecret(dc))
	if d2.Status != "approved" || d2.UserID != u.ID {
		t.Fatalf("approved = %+v", d2)
	}
	s.DeleteDevice(d2.CodeHash)
	if _, err := s.GetDeviceByHash(auth.HashSecret(dc)); err == nil {
		t.Fatal("expected deleted device")
	}
}

func TestFunctionsOwnerSlug(t *testing.T) {
	s := openTest(t)
	h, _ := auth.HashPassword("password123")
	u, _ := s.CreateUser("alice", h)
	if err := s.UpsertFunctionOwned(u.ID, "hello", "v1", "name=\"hello\"", "abcd1234", "key"); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFunction("hello")
	if err != nil {
		t.Fatal(err)
	}
	if f.OwnerID != u.ID || f.Slug != "abcd1234" || f.AuthMode != "key" {
		t.Fatalf("fn = %+v", f)
	}
	bySlug, err := s.GetFunctionBySlug("abcd1234")
	if err != nil || bySlug.Name != "hello" {
		t.Fatalf("bySlug = %+v, err = %v", bySlug, err)
	}
	byNS, err := s.GetFunctionNamespaced("alice", "hello")
	if err != nil || byNS.Name != "hello" {
		t.Fatalf("byNS = %+v, err = %v", byNS, err)
	}
	owned, err := s.ListFunctionsByOwner(u.ID)
	if err != nil || len(owned) != 1 {
		t.Fatalf("owned = %+v, err = %v", owned, err)
	}
	other, err := s.ListFunctionsByOwner("nobody")
	if err != nil || len(other) != 0 {
		t.Fatalf("other should be empty: %+v", other)
	}
}

func TestAPIKeys(t *testing.T) {
	s := openTest(t)
	h, _ := auth.HashPassword("password123")
	u, _ := s.CreateUser("alice", h)
	full, prefix, hash, _ := auth.NewAPIKey()
	k, err := s.CreateAPIKey(u.ID, "hello", "ci", prefix, hash)
	if err != nil {
		t.Fatal(err)
	}
	_ = full
	if _, ok := s.FindAPIKey("hello", hash); !ok {
		t.Fatal("key not found")
	}
	ks, err := s.ListAPIKeys(u.ID, "hello")
	if err != nil || len(ks) != 1 || ks[0].ID != k.ID {
		t.Fatalf("list = %+v, err = %v", ks, err)
	}
	if err := s.RevokeAPIKey(u.ID, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FindAPIKey("hello", hash); ok {
		t.Fatal("revoked key still valid")
	}
	if err := s.RevokeAPIKey(u.ID, k.ID); err == nil {
		t.Fatal("expected double-revoke error")
	}
}
