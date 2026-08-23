package civil

import (
	"testing"
	"time"
)

func TestDateOfUsesUTC(t *testing.T) {
	// 23:30 in UTC-5 is 04:30 next day UTC.
	loc := time.FixedZone("m5", -5*3600)
	d := DateOf(time.Date(2026, 8, 22, 23, 30, 0, 0, loc))
	if d.String() != "2026-08-23" {
		t.Fatalf("got %s, want 2026-08-23", d)
	}
}

func TestParseRoundTrip(t *testing.T) {
	d, err := Parse("2026-02-28")
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "2026-02-28" {
		t.Fatalf("round trip got %s", d)
	}
	if _, err := Parse("2026-13-01"); err == nil {
		t.Fatal("invalid month must error")
	}
	if _, err := Parse("garbage"); err == nil {
		t.Fatal("garbage must error")
	}
}

func TestAddDaysCrossesMonth(t *testing.T) {
	d, _ := Parse("2026-01-30")
	if got := d.AddDays(3).String(); got != "2026-02-02" {
		t.Fatalf("got %s, want 2026-02-02", got)
	}
	if got := d.AddDays(-30).String(); got != "2025-12-31" {
		t.Fatalf("got %s, want 2025-12-31", got)
	}
}

func TestBefore(t *testing.T) {
	a, _ := Parse("2026-08-01")
	b, _ := Parse("2026-08-02")
	if !a.Before(b) || b.Before(a) || a.Before(a) {
		t.Fatal("Before ordering wrong")
	}
}

func TestTimeIsMidnightUTC(t *testing.T) {
	d, _ := Parse("2026-08-22")
	want := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if !d.Time().Equal(want) {
		t.Fatalf("got %v", d.Time())
	}
}
