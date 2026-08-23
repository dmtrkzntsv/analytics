package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeMeta is an in-memory MetaStore. getErr/setErr inject a failure for one
// specific key so store errors can be exercised per call site.
type fakeMeta struct {
	m      map[string]string
	getErr map[string]error
	setErr map[string]error
}

func (f *fakeMeta) GetMeta(_ context.Context, k string) (string, error) {
	if err := f.getErr[k]; err != nil {
		return "", err
	}
	return f.m[k], nil
}

func (f *fakeMeta) SetMeta(_ context.Context, k, v string) error {
	if err := f.setErr[k]; err != nil {
		return err
	}
	f.m[k] = v
	return nil
}

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

// A restarted process must adopt the persisted salt rather than minting a new
// one; rotating on every restart would fragment visitor identity mid-day.
func TestSalterAdoptsPersistedSaltOnRestart(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	meta := &fakeMeta{m: map[string]string{}}
	first, err := NewSalter(meta, func() time.Time { return now }).Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(3 * time.Hour)
	restarted, err := NewSalter(meta, func() time.Time { return now }).Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restarted != first {
		t.Fatal("restart within the rotation window must reuse the persisted salt")
	}

	// Past the window, a restarted process still rotates.
	now = now.Add(25 * time.Hour)
	rotated, err := NewSalter(meta, func() time.Time { return now }).Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rotated == first {
		t.Fatal("restart past the rotation window must rotate")
	}
}

// An unparseable persisted timestamp must not wedge the salter into serving a
// salt of unknown age; it falls back to rotating.
func TestSalterRotatesOnCorruptTimestamp(t *testing.T) {
	meta := &fakeMeta{m: map[string]string{
		"visitor_salt":            "stale",
		"visitor_salt_rotated_at": "not-a-timestamp",
	}}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	salt, err := NewSalter(meta, func() time.Time { return now }).Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if salt == "stale" {
		t.Fatal("salt with an unparseable timestamp must be replaced")
	}
	if meta.m["visitor_salt_rotated_at"] != now.Format(time.RFC3339) {
		t.Fatalf("rotation time not repaired: %q", meta.m["visitor_salt_rotated_at"])
	}
}

// Rotate forces a new salt regardless of age, destroying the old one. The jobs
// scheduler calls this on the daily boundary.
func TestSalterRotateForcesNewSalt(t *testing.T) {
	meta := &fakeMeta{m: map[string]string{}}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	s := NewSalter(meta, func() time.Time { return now })
	ctx := context.Background()
	first, err := s.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := s.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("Rotate must replace the salt even inside the rotation window")
	}
	if meta.m["visitor_salt"] != second {
		t.Fatal("old salt must be overwritten in the store, not retained")
	}
}

func TestSalterPropagatesStoreErrors(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC) }
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		meta *fakeMeta
	}{
		{"get salt", &fakeMeta{m: map[string]string{}, getErr: map[string]error{"visitor_salt": boom}}},
		{"get rotated_at", &fakeMeta{m: map[string]string{}, getErr: map[string]error{"visitor_salt_rotated_at": boom}}},
		{"set salt", &fakeMeta{m: map[string]string{}, setErr: map[string]error{"visitor_salt": boom}}},
		{"set rotated_at", &fakeMeta{m: map[string]string{}, setErr: map[string]error{"visitor_salt_rotated_at": boom}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			salt, err := NewSalter(tc.meta, now).Current(context.Background())
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want boom", err)
			}
			if salt != "" {
				t.Fatalf("salt = %q, want empty on error", salt)
			}
		})
	}
}
