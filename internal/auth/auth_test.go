package auth

import (
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct-horse-9")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct-horse-9") {
		t.Fatal("valid password rejected")
	}
	if CheckPassword(h, "wrong") {
		t.Fatal("invalid password accepted")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected short password error")
	}
}

func TestOperatorTokenUnique(t *testing.T) {
	a, ha, err := NewOperatorToken()
	if err != nil {
		t.Fatal(err)
	}
	b, hb, err := NewOperatorToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || ha == hb {
		t.Fatal("tokens must be unique")
	}
	if !strings.HasPrefix(a, "act_") {
		t.Fatalf("prefix = %q", a)
	}
	if !CheckSecret(ha, a) || CheckSecret(ha, b) {
		t.Fatal("secret check wrong")
	}
}

func TestAPIKeyShape(t *testing.T) {
	full, prefix, hash, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full, "ak_"+prefix+"_") {
		t.Fatalf("key %q missing prefix %q", full, prefix)
	}
	if !CheckSecret(hash, full) {
		t.Fatal("key hash mismatch")
	}
}

func TestUserCodeAndSlug(t *testing.T) {
	c, err := NewUserCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 9 || c[4] != '-' {
		t.Fatalf("user_code = %q", c)
	}
	s, err := NewSlug()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 8 {
		t.Fatalf("slug = %q", s)
	}
}

func TestModes(t *testing.T) {
	if NormalizeMode("") != ModePublic || NormalizeMode("KEY") != ModeKey {
		t.Fatal("normalize wrong")
	}
	if err := ValidateMode("token"); err == nil {
		t.Fatal("expected invalid mode error")
	}
	if err := ValidateUsername("ab"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUsername("A!"); err == nil {
		t.Fatal("expected username error")
	}
	if err := ValidateFunctionName("hello-world_1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFunctionName("a/b"); err == nil {
		t.Fatal("expected function name error")
	}
	if Bearer("Bearer act_x") != "act_x" {
		t.Fatal("bearer parse wrong")
	}
}
