package artifacts

import (
	"context"
	"testing"
)

func TestLocalRoundTrip(t *testing.T) {
	l := NewLocal(t.TempDir())
	ctx := context.Background()
	if err := l.Put(ctx, BundleKey("hello", "v1", "src.zip"), []byte("zipbytes")); err != nil {
		t.Fatal(err)
	}
	got, err := l.Get(ctx, BundleKey("hello", "v1", "src.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "zipbytes" {
		t.Fatalf("got %q", got)
	}
}
