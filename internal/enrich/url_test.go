package enrich

import "testing"

func TestParsePageURL(t *testing.T) {
	p, err := ParsePageURL("https://app.com/pricing?utm_source=hn&utm_medium=social&utm_campaign=launch&ref=newsletter&session_token=SECRET&email=a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "app.com" || p.Path != "/pricing" {
		t.Errorf("host/path: %+v", p)
	}
	if p.UTMSource != "hn" || p.UTMMedium != "social" || p.UTMCampaign != "launch" || p.Ref != "newsletter" {
		t.Errorf("campaign params: %+v", p)
	}
	// Privacy: nothing else from the query may survive anywhere in the struct.
	if p.Path != "/pricing" {
		t.Errorf("query must not leak into path: %q", p.Path)
	}
	if _, err := ParsePageURL("not a url"); err == nil {
		t.Error("garbage must error")
	}
	if _, err := ParsePageURL("/relative/only"); err == nil {
		t.Error("relative URL must error (no host)")
	}
	if p, _ := ParsePageURL("https://app.com"); p.Path != "/" {
		t.Errorf("empty path must normalize to /: %q", p.Path)
	}
}

func TestCleanReferrer(t *testing.T) {
	cases := []struct{ ref, own, want string }{
		{"", "app.com", ""},
		{"https://app.com/other-page", "app.com", ""},              // own-domain
		{"https://www.google.com/search?q=x", "app.com", "google"}, // engine
		{"https://google.de/url", "app.com", "google"},
		{"https://news.ycombinator.com/item?id=1", "app.com", "hackernews"},
		{"https://x.com/somebody", "app.com", "twitter"},
		{"https://t.co/abc", "app.com", "twitter"},
		{"https://www.example.org/blog", "app.com", "example.org"}, // generic: bare host
		{"::not-a-url::", "app.com", ""},
	}
	for _, c := range cases {
		if got := CleanReferrer(c.ref, c.own); got != c.want {
			t.Errorf("CleanReferrer(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
