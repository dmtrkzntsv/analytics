package enrich

import (
	"fmt"
	"net/url"
	"strings"
)

type PageInfo struct {
	Host, Path                             string
	UTMSource, UTMMedium, UTMCampaign, Ref string
}

func ParsePageURL(raw string) (PageInfo, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return PageInfo{}, fmt.Errorf("enrich: not an absolute http(s) URL: %q", raw)
	}
	q := u.Query()
	path := u.Path
	if path == "" {
		path = "/"
	}
	// Only campaign parameters survive; the rest of the query (and any
	// fragment) is discarded here and never stored (spec §5.4/§5.4a).
	return PageInfo{
		Host:        u.Hostname(),
		Path:        path,
		UTMSource:   q.Get("utm_source"),
		UTMMedium:   q.Get("utm_medium"),
		UTMCampaign: q.Get("utm_campaign"),
		Ref:         q.Get("ref"),
	}, nil
}

// referrerNames maps exact hostnames (after www-stripping) to sources.
var referrerNames = map[string]string{
	"t.co": "twitter", "twitter.com": "twitter", "x.com": "twitter",
	"facebook.com": "facebook", "l.facebook.com": "facebook", "lm.facebook.com": "facebook",
	"linkedin.com": "linkedin", "lnkd.in": "linkedin",
	"reddit.com": "reddit", "old.reddit.com": "reddit",
	"news.ycombinator.com": "hackernews",
	"instagram.com":        "instagram",
	"youtube.com":          "youtube", "youtu.be": "youtube",
	"duckduckgo.com": "duckduckgo",
}

// referrerPrefixes catches localized engine domains (google.de, yahoo.co.jp).
var referrerPrefixes = map[string]string{
	"google.": "google", "bing.": "bing", "yahoo.": "yahoo",
	"baidu.": "baidu", "yandex.": "yandex",
}

func CleanReferrer(referrer, ownHost string) string {
	if referrer == "" {
		return ""
	}
	u, err := url.Parse(referrer)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == strings.TrimPrefix(strings.ToLower(ownHost), "www.") {
		return ""
	}
	if name, ok := referrerNames[host]; ok {
		return name
	}
	for prefix, name := range referrerPrefixes {
		if strings.HasPrefix(host, prefix) {
			return name
		}
	}
	return host
}
