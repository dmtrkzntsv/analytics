package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dmitry/analytics/internal/config/configtest"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/store"
)

type fakeQueue struct {
	mu     sync.Mutex
	hits   []store.WebHit
	events []store.ProductEvent
}

func (f *fakeQueue) EnqueueHit(h store.WebHit) {
	f.mu.Lock()
	f.hits = append(f.hits, h)
	f.mu.Unlock()
}
func (f *fakeQueue) EnqueueEvent(e store.ProductEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

type fixedSalt struct{}

func (fixedSalt) Current(context.Context) (string, error) { return "test-salt", nil }

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func testServer(t *testing.T) (*fakeQueue, http.Handler) {
	t.Helper()
	cfg := configtest.Load(t, nil,
		`[{"alias": "app", "name": "App", "ingest_keys": [{"key": "ak_test", "label": "web"}], "allowed_origins": ["https://app.com"]}]`)
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	return q, New(cfg, q, g, fixedSalt{}, slog.Default())
}

func postHit(h http.Handler, origin, body, ua string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/hit", strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("User-Agent", ua)
	r.Header.Set("CF-IPCountry", "DE")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHitHappyPath(t *testing.T) {
	q, h := testServer(t)
	w := postHit(h, "https://app.com",
		`{"project":"app","url":"https://app.com/pricing?utm_source=hn&secret=x","referrer":"https://news.ycombinator.com/"}`,
		chromeUA)
	if w.Code != 202 {
		t.Fatalf("code = %d body %s", w.Code, w.Body)
	}
	if len(q.hits) != 1 {
		t.Fatalf("hits = %d", len(q.hits))
	}
	hit := q.hits[0]
	if hit.Path != "/pricing" || hit.UTMSource != "hn" || hit.ReferrerSource != "hackernews" ||
		hit.Country != "DE" || hit.Device != "desktop" || hit.Browser != "Chrome" || hit.OS != "Windows" {
		t.Errorf("hit = %+v", hit)
	}
	if hit.ID == "" || hit.VisitorHash == "" || len(hit.VisitorHash) != 16 {
		t.Errorf("id/hash: %+v", hit)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://app.com" {
		t.Error("CORS header missing on allowed origin")
	}
}

func TestHitRejectsBadOrigin(t *testing.T) {
	q, h := testServer(t)
	if w := postHit(h, "https://evil.com", `{"project":"app","url":"https://app.com/"}`, chromeUA); w.Code != 403 {
		t.Fatalf("evil origin: code = %d", w.Code)
	}
	if w := postHit(h, "", `{"project":"app","url":"https://app.com/"}`, chromeUA); w.Code != 403 {
		t.Fatalf("missing origin on /api/hit: code = %d", w.Code)
	}
	if len(q.hits) != 0 {
		t.Fatal("rejected hits must not enqueue")
	}
}

func TestHitUnknownProjectSilentDrop(t *testing.T) {
	q, h := testServer(t)
	w := postHit(h, "https://app.com", `{"project":"nope","url":"https://app.com/"}`, chromeUA)
	if w.Code != 204 {
		t.Fatalf("code = %d, want 204 (no oracle, spec §5.2)", w.Code)
	}
	if len(q.hits) != 0 {
		t.Fatal("unknown project must not enqueue")
	}
}

func TestHitDropsBots(t *testing.T) {
	q, h := testServer(t)
	w := postHit(h, "https://app.com", `{"project":"app","url":"https://app.com/"}`,
		"Mozilla/5.0 (compatible; Googlebot/2.1)")
	if w.Code != 202 || len(q.hits) != 0 {
		t.Fatalf("bots: code=%d hits=%d (want 202, 0)", w.Code, len(q.hits))
	}
}

func TestHitBadPayloads(t *testing.T) {
	_, h := testServer(t)
	for name, body := range map[string]string{
		"not json":   "{",
		"no url":     `{"project":"app"}`,
		"relative":   `{"project":"app","url":"/x"}`,
		"no project": `{"url":"https://app.com/"}`,
	} {
		if w := postHit(h, "https://app.com", body, chromeUA); w.Code != 400 {
			t.Errorf("%s: code = %d, want 400", name, w.Code)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	_, h := testServer(t)
	big := `{"project":"app","url":"https://app.com/","referrer":"` + strings.Repeat("x", 17*1024) + `"}`
	if w := postHit(h, "https://app.com", big, chromeUA); w.Code != 400 && w.Code != 413 {
		t.Fatalf("oversize body: code = %d", w.Code)
	}
}

func TestEventNoOriginAllowed(t *testing.T) {
	q, h := testServer(t)
	r := httptest.NewRequest("POST", "/api/event", strings.NewReader(
		`{"project":"app","name":"subscribed","user_id":"u1","attributes":{"plan":"pro","n":7}}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || len(q.events) != 1 {
		t.Fatalf("server-side event: code=%d events=%d", w.Code, len(q.events))
	}
	e := q.events[0]
	if e.EventName != "subscribed" || e.UserID != "u1" || e.Attributes["plan"] != "pro" || e.Attributes["n"] != "7" {
		t.Errorf("event = %+v (non-string attr must stringify)", e)
	}
}

func TestEventAttributeLimits(t *testing.T) {
	q, h := testServer(t)
	attrs := map[string]any{strings.Repeat("k", 65): "dropped-key", "ok": strings.Repeat("v", 600)}
	body, _ := json.Marshal(map[string]any{"project": "app", "name": "e", "user_id": "u", "attributes": attrs})
	r := httptest.NewRequest("POST", "/api/event", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("code = %d", w.Code)
	}
	e := q.events[0]
	if _, exists := e.Attributes[strings.Repeat("k", 65)]; exists {
		t.Error("over-long key must be dropped")
	}
	if len(e.Attributes["ok"]) != 512 {
		t.Errorf("value must truncate to 512, got %d", len(e.Attributes["ok"]))
	}
}

func TestPreflight(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("OPTIONS", "/api/event", nil)
	r.Header.Set("Origin", "https://app.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.com" {
		t.Fatalf("preflight allowed origin: %d %v", w.Code, w.Header())
	}
	r2 := httptest.NewRequest("OPTIONS", "/api/event", nil)
	r2.Header.Set("Origin", "https://evil.com")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("preflight must not allow unknown origins")
	}
}

func TestHealthz(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("healthz = %d", w.Code)
	}
}
