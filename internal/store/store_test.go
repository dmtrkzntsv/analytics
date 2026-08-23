package store

import "testing"

func TestOpenUnknownScheme(t *testing.T) {
	if _, err := Open("bogus:///x"); err == nil {
		t.Fatal("unknown scheme must error")
	}
}

func TestRegisterAndOpen(t *testing.T) {
	called := ""
	Register("fake", func(dsn string) (Store, error) { called = dsn; return nil, nil })
	if _, err := Open("fake:///db"); err != nil {
		t.Fatal(err)
	}
	if called != "fake:///db" {
		t.Fatalf("factory got %q", called)
	}
}

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := Open("://"); err == nil {
		t.Fatal("invalid DSN must error")
	}
}
