package sidecar

import (
	"testing"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	a := r.Mint("f1")
	b := r.Mint("f1")
	if !r.Check("f1", a) || !r.Check("f1", b) {
		t.Fatal("tokens should validate")
	}
	if r.Check("f2", a) {
		t.Fatal("cross-function token accepted")
	}
	if r.Check("f1", "bogus") || r.Check("", a) || r.Check("f1", "") {
		t.Fatal("empty/bogus accepted")
	}
	if !r.ValidAny(a) || r.ValidAny("bogus") {
		t.Fatal("ValidAny wrong")
	}
	r.Revoke("f1", a)
	if r.Check("f1", a) || !r.Check("f1", b) {
		t.Fatal("revoke wrong")
	}
	r.Revoke("f1", b)
	if r.ValidAny(b) {
		t.Fatal("empty set should vanish")
	}
}
