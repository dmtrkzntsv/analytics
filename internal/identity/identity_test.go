package identity

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeMeta struct{ m map[string]string }

func (f *fakeMeta) GetMeta(_ context.Context, k string) (string, error) { return f.m[k], nil }
func (f *fakeMeta) SetMeta(_ context.Context, k, v string) error        { f.m[k] = v; return nil }

func TestVisitorHashProperties(t *testing.T) {
	h1 := VisitorHash("salt", "1.2.3.4", "UA", "app")
	if len(h1) != 16 || strings.ToLower(h1) != h1 {
		t.Fatalf("hash %q must be 16 lowercase hex chars", h1)
	}
	if h1 != VisitorHash("salt", "1.2.3.4", "UA", "app") {
		t.Fatal("must be deterministic")
	}
	for _, other := range []string{
		VisitorHash("salt2", "1.2.3.4", "UA", "app"),
		VisitorHash("salt", "1.2.3.5", "UA", "app"),
		VisitorHash("salt", "1.2.3.4", "UA2", "app"),
		VisitorHash("salt", "1.2.3.4", "UA", "app2"),
	} {
		if other == h1 {
			t.Fatal("any component change must change the hash")
		}
	}
	// Concatenation ambiguity: (a,bc) vs (ab,c) must differ.
	if VisitorHash("s", "ab", "c", "p") == VisitorHash("s", "a", "bc", "p") {
		t.Fatal("components must be delimited")
	}
}

func TestSalterGeneratesAndPersists(t *testing.T) {
	meta := &fakeMeta{m: map[string]string{}}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	s := NewSalter(meta, func() time.Time { return now })
	salt, err := s.Current(context.Background())
	if err != nil || len(salt) < 32 {
		t.Fatalf("salt %q err %v", salt, err)
	}
	again, _ := s.Current(context.Background())
	if again != salt {
		t.Fatal("salt must be stable within a day")
	}
}

func TestSalterRotatesAfter24h(t *testing.T) {
	meta := &fakeMeta{m: map[string]string{}}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	s := NewSalter(meta, func() time.Time { return now })
	first, _ := s.Current(context.Background())
	now = now.Add(25 * time.Hour)
	second, err := s.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("salt must rotate after 24h")
	}
	if meta.m["visitor_salt"] != second {
		t.Fatal("new salt must be persisted (old destroyed)")
	}
}
