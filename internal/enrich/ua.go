// Package enrich derives coarse device/browser/os classes and cleans
// referrers/URLs at ingest. Only substring matching on the hot path
// (spec §12a) — order of checks matters and is documented inline.
package enrich

import "strings"

var botMarkers = []string{
	"bot", "crawler", "spider", "crawling", "headless", "lighthouse",
	"slurp", "curl/", "wget/", "python-requests", "facebookexternalhit", "preview",
}

func IsBot(ua string) bool {
	if ua == "" {
		return true
	}
	l := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

func ParseUserAgent(ua string) (device, browser, os string) {
	// OS first — some browser checks depend on it.
	switch {
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	// Browser: check derivatives before their bases (Edge/Samsung before
	// Chrome, Chrome before Safari — every Chrome UA contains "Safari").
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "Edge/"):
		browser = "Edge"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera/"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"):
		device = "tablet"
	case strings.Contains(ua, "Mobile"), strings.Contains(ua, "iPhone"):
		device = "mobile"
	default:
		device = "desktop"
	}
	return device, browser, os
}
