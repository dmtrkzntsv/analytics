package geo

import (
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestUnknownScheme(t *testing.T) {
	if _, err := New("nope://", t.TempDir(), slog.Default()); err == nil {
		t.Fatal("unknown scheme must error")
	}
}

func TestInvalidDSNRejected(t *testing.T) {
	// No "://" at all: url.Parse succeeds but leaves Scheme empty.
	if _, err := New("not-a-dsn", t.TempDir(), slog.Default()); err == nil {
		t.Fatal("DSN without a scheme must error")
	}
	// A control character makes url.Parse itself fail.
	if _, err := New("cloud\x7fflare://", t.TempDir(), slog.Default()); err == nil {
		t.Fatal("unparseable DSN must error")
	}
}

func TestCloudflareProvider(t *testing.T) {
	p, err := New("cloudflare://", t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cases := map[string]string{"DE": "DE", "de": "DE", "XX": "", "T1": "", "": ""}
	for header, want := range cases {
		r := httptest.NewRequest("POST", "/api/hit", nil)
		if header != "" {
			r.Header.Set("CF-IPCountry", header)
		}
		if got := p.Country(r, "1.2.3.4"); got != want {
			t.Errorf("header %q => %q, want %q", header, got, want)
		}
	}
}

func TestNoneProvider(t *testing.T) {
	p, err := New("none://", t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if p.Country(r, "8.8.8.8") != "" {
		t.Fatal("none provider must return empty")
	}
	if err := p.Close(); err != nil {
		t.Errorf("none.Close() = %v, want nil", err)
	}
}
