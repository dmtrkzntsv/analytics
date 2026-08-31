package enrich

import "testing"

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

// Referrer cleaning uses the host the client supplied. Same host means a
// self-referral and is suppressed; no host means there is nothing to
// compare against, so the referrer is taken at face value.
func TestCleanReferrerAgainstSuppliedHost(t *testing.T) {
	for _, tc := range []struct {
		name, host, referrer, want string
	}{
		{"self", "shop.example.com", "https://shop.example.com/a", ""},
		{"external", "shop.example.com", "https://news.ycombinator.com/", "hackernews"},
		{"no host", "", "https://shop.example.com/a", "shop.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanReferrer(tc.referrer, tc.host)
			if got != tc.want {
				t.Errorf("CleanReferrer(%q, %q) = %q, want %q",
					tc.referrer, tc.host, got, tc.want)
			}
		})
	}
}
