// Package geo resolves a request's country. Providers are selected by DSN
// scheme (spec §6): cloudflare:// (default), maxmind://KEY, none://.
package geo

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

type Provider interface {
	Country(r *http.Request, ip string) string
	Close() error
}

type factory func(u *url.URL, dataDir string, logger *slog.Logger) (Provider, error)

var providers = map[string]factory{
	"cloudflare": func(*url.URL, string, *slog.Logger) (Provider, error) { return cloudflare{}, nil },
	"none":       func(*url.URL, string, *slog.Logger) (Provider, error) { return none{}, nil },
}

func New(dsn, dataDir string, logger *slog.Logger) (Provider, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("geo: invalid DSN %q", dsn)
	}
	f, ok := providers[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("geo: unknown provider %q (supported: cloudflare, maxmind, none)", u.Scheme)
	}
	return f(u, dataDir, logger)
}

type cloudflare struct{}

func (cloudflare) Country(r *http.Request, _ string) string {
	c := strings.ToUpper(r.Header.Get("CF-IPCountry"))
	if c == "" || c == "XX" || c == "T1" {
		return ""
	}
	return c
}
func (cloudflare) Close() error { return nil }

type none struct{}

func (none) Country(*http.Request, string) string { return "" }
func (none) Close() error                         { return nil }
