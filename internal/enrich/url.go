package enrich

import (
	"net/url"
	"strings"
)

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
