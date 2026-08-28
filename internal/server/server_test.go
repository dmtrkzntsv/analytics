package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/config/configtest"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

type fakeQueue struct {
	mu         sync.Mutex
	hits       []store.WebHit
	events     []store.ProductEvent
	views      []store.AppView
	identities []store.Identity
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
func (f *fakeQueue) EnqueueAppView(v store.AppView) {
	f.mu.Lock()
	f.views = append(f.views, v)
	f.mu.Unlock()
}

// UpsertIdentities lets the queue double as the NameStore, so a test can
// assert on events and names together.
func (f *fakeQueue) UpsertIdentities(_ context.Context, ids []store.Identity) error {
	f.mu.Lock()
	f.identities = append(f.identities, ids...)
	f.mu.Unlock()
	return nil
}

type fixedSalt struct{}

func (fixedSalt) Current(context.Context) (string, error) { return "test-salt", nil }

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

const (
	testKey    = "ak_test"
	testOrigin = "https://app.com"
)

func testServer(t *testing.T) (*fakeQueue, http.Handler) {
	return testServerWithIdentity(t, "anonymous")
}

// newTestRegistry seeds a temp-DB registry with the given projects.
// Replaces the old inline cfg.Projects construction.
func newTestRegistry(t *testing.T, cfg *config.Config, projects []manage.ProjectSpec, keys map[string][2]string) *manage.Registry {
	t.Helper()
	st, err := store.Open("sqlite://" + t.TempDir() + "/reg.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, cfg.Retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	for _, spec := range projects {
		if _, err := ops.CreateProject(ctx, "test", spec); err != nil {
			t.Fatal(err)
		}
	}
	for project, kl := range keys { // project -> {key, label}
		if err := st.InsertIngestKey(ctx, store.RegistryKey{
			Key: kl[0], Project: project, Label: kl[1]},
			store.AuditEntry{Actor: "test", Action: "key.issue", Subject: kl[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return reg
}

func testServerWithIdentity(t *testing.T, mode string) (*fakeQueue, http.Handler) {
	t.Helper()
	cfg := configtest.Load(t, nil)
	reg := newTestRegistry(t, cfg,
		[]manage.ProjectSpec{{
			Alias: "app", Name: "App", Identity: mode,
			AllowedOrigins: []string{testOrigin},
		}},
		map[string][2]string{"app": {testKey, "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	return q, New(cfg, reg, q, g, fixedSalt{}, q, slog.Default())
}

// post sends one envelope. headers may set Origin, X-Analytics-Key or a
// non-browser User-Agent; a Chrome UA and CF country are the defaults so
// $pageview enrichment behaves like a real browser request.
func post(h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/events", strings.NewReader(body))
	r.Header.Set("User-Agent", chromeUA)
	r.Header.Set("CF-IPCountry", "DE")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeResult(t *testing.T, w *httptest.ResponseRecorder) ingestResult {
	t.Helper()
	var res ingestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return res
}

func envelopeOf(events string) string {
	return `{"key":"` + testKey + `","events":[` + events + `]}`
}

// --- authentication ---

func TestRejectsUnknownKey(t *testing.T) {
	_, h := testServer(t)
	w := post(h, `{"key":"nope","events":[{"name":"x"}]}`, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRejectsMissingKey(t *testing.T) {
	_, h := testServer(t)
	w := post(h, `{"events":[{"name":"x"}]}`, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAcceptsKeyFromHeader(t *testing.T) {
	q, h := testServer(t)
	w := post(h, `{"events":[{"name":"subscribed","attributes":{"plan":"pro"}}]}`,
		map[string]string{"X-Analytics-Key": testKey})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(q.events) != 1 || q.events[0].EventName != "subscribed" {
		t.Fatalf("queued = %+v", q.events)
	}
	if q.events[0].Attributes["plan"] != "pro" {
		t.Errorf("attributes = %v", q.events[0].Attributes)
	}
	if q.events[0].Project != "app" {
		t.Errorf("project = %q; the key must resolve it", q.events[0].Project)
	}
}

func TestHeaderKeyBeatsBodyKey(t *testing.T) {
	_, h := testServer(t)
	w := post(h, `{"key":"wrong","events":[{"name":"x"}]}`,
		map[string]string{"X-Analytics-Key": testKey})
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}
}

// --- routing ---

func TestRoutesPageviewScreenViewAndCustom(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$platform":"ios","$app_version":"2.4.1"},
	  "events":[
	    {"name":"$pageview","attributes":{"$url":"https://app.com/pricing?utm_source=hn"}},
	    {"name":"$screen_view","attributes":{"$screen":"/settings","$os_version":"17.2","$device_model":"iPhone15,2","$locale":"en-US","$session_id":"s1"}},
	    {"name":"subscribed","attributes":{"plan":"pro"}}
	  ]}`
	w := post(h, body, map[string]string{"Origin": testOrigin})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if res := decodeResult(t, w); res.Accepted != 3 || res.Rejected != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(q.hits) != 1 || q.hits[0].Path != "/pricing" || q.hits[0].UTMSource != "hn" {
		t.Errorf("hits = %+v", q.hits)
	}
	if q.hits[0].Country != "DE" || q.hits[0].Browser != "Chrome" {
		t.Errorf("hit enrichment = %+v", q.hits[0])
	}
	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	v := q.views[0]
	if v.Screen != "/settings" || v.AppVersion != "2.4.1" || v.Platform != "ios" ||
		v.OSVersion != "17.2" || v.DeviceModel != "iPhone15,2" || v.Locale != "en-US" ||
		v.SessionID != "s1" || v.Country != "DE" {
		t.Errorf("view = %+v", v)
	}
	if len(q.events) != 1 || q.events[0].Platform != "ios" || q.events[0].AppVersion != "2.4.1" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestPerEventContextOverridesBatch(t *testing.T) {
	q, h := testServer(t)
	body := `{"key":"` + testKey + `","attributes":{"$app_version":"2.4.1"},
	  "events":[{"name":"a"},{"name":"b","attributes":{"$app_version":"2.5.0"}}]}`
	post(h, body, nil)
	if len(q.events) != 2 {
		t.Fatalf("events = %+v", q.events)
	}
	if q.events[0].AppVersion != "2.4.1" || q.events[1].AppVersion != "2.5.0" {
		t.Errorf("versions = %q %q; want 2.4.1 then 2.5.0",
			q.events[0].AppVersion, q.events[1].AppVersion)
	}
}

func TestUnknownReservedNameBecomesCustomEventWithWarning(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"$pageviews"}`), nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}
	res := decodeResult(t, w)
	if res.Accepted != 1 || res.Rejected != 0 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v", res)
	}
	if len(q.events) != 1 || q.events[0].EventName != "$pageviews" {
		t.Errorf("events = %+v; an unknown reserved name must not be dropped", q.events)
	}
}

func TestUnknownReservedKeyDroppedWithWarning(t *testing.T) {
	q, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"x","attributes":{"$app_ver":"2","plan":"pro"}}`), nil)
	res := decodeResult(t, w)
	if res.Accepted != 1 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v", res)
	}
	if _, ok := q.events[0].Attributes["$app_ver"]; ok {
		t.Error("unknown reserved key must not be stored")
	}
	if q.events[0].Attributes["plan"] != "pro" {
		t.Error("ordinary attributes must survive")
	}
}

// --- per-event rejection ---

func TestPerEventRejectionLeavesBatchIntact(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"good"},{"name":""},{"name":"$pageview"},
		{"name":"$screen_view"},{"name":"bad-id","id":"not-a-uuid"},{"name":"also_good"}`)
	w := post(h, body, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even with rejects", w.Code)
	}
	res := decodeResult(t, w)
	// missing name; $pageview without $url; $screen_view without $screen; bad id
	if res.Accepted != 2 || res.Rejected != 4 || len(res.Errors) != 4 {
		t.Errorf("result = %+v", res)
	}
	if len(q.events) != 2 {
		t.Errorf("queued %d events, want 2", len(q.events))
	}
}

func TestClientSuppliedIDIsPreserved(t *testing.T) {
	q, h := testServer(t)
	post(h, envelopeOf(`{"name":"x","id":"018f1e5a-0000-7000-8000-000000000001"}`), nil)
	if len(q.events) != 1 || q.events[0].ID != "018f1e5a-0000-7000-8000-000000000001" {
		t.Errorf("events = %+v; a client id must survive so retries dedupe", q.events)
	}
}

func TestMissingIDIsGenerated(t *testing.T) {
	q, h := testServer(t)
	post(h, envelopeOf(`{"name":"x"}`), nil)
	if len(q.events) != 1 || q.events[0].ID == "" {
		t.Errorf("events = %+v", q.events)
	}
}

func TestClientTimestampIsUsedAndClamped(t *testing.T) {
	q, h := testServer(t)
	post(h, envelopeOf(`{"name":"recent","ts":"2026-08-23T10:00:00Z"},
		{"name":"ancient","ts":"2001-01-01T00:00:00Z"}`), nil)
	if len(q.events) != 2 {
		t.Fatalf("events = %+v", q.events)
	}
	if !q.events[0].TS.Equal(time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("in-range ts = %v, want the client value", q.events[0].TS)
	}
	// Clamped, not dropped, and never older than the app raw window.
	if q.events[1].TS.Before(q.events[1].ReceivedAt.Add(-31 * 24 * time.Hour)) {
		t.Errorf("clamped ts = %v, older than the raw window", q.events[1].TS)
	}
}

func TestOversizedBatchIsRejected(t *testing.T) {
	_, h := testServer(t)
	var b strings.Builder
	b.WriteString(`{"key":"` + testKey + `","events":[`)
	for i := 0; i < maxBatchEvents+1; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"x"}`)
	}
	b.WriteString(`]}`)
	if w := post(h, b.String(), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	_, h := testServer(t)
	big := strings.Repeat("a", maxBody+1024)
	if w := post(h, `{"key":"`+testKey+`","events":[{"name":"`+big+`"}]}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an oversized body", w.Code)
	}
}

func TestMalformedBodyIsRejected(t *testing.T) {
	_, h := testServer(t)
	if w := post(h, `not json`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- origin ---

func TestOriginPresentMustMatch(t *testing.T) {
	_, h := testServer(t)
	w := post(h, envelopeOf(`{"name":"x"}`), map[string]string{"Origin": "https://evil.example"})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestOriginAbsentIsAccepted(t *testing.T) {
	_, h := testServer(t)
	if w := post(h, envelopeOf(`{"name":"x"}`), nil); w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202 for a native app with no Origin", w.Code)
	}
}

// --- identity modes ---

func TestAnonymousModeHashesIdentifiers(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$group_id":"org9","$user_name":"Ada","$group_name":"Acme","$install_id":"i1"},
	  "events":[{"name":"$screen_view","attributes":{"$screen":"/x"}}]}`, nil)

	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	v := q.views[0]
	if v.UserID == "u1" || v.UserID == "" {
		t.Errorf("user_id = %q; want a salted hash", v.UserID)
	}
	if v.ActorID == "u1" || v.ActorID == "i1" || len(v.ActorID) != 16 {
		t.Errorf("actor_id = %q; want a salted hash", v.ActorID)
	}
	if v.GroupID != "org9" {
		t.Errorf("group_id = %q; groups stay raw in both modes", v.GroupID)
	}
	if len(q.identities) != 1 || q.identities[0].Kind != store.KindGroup {
		t.Errorf("identities = %+v; $user_name must be ignored in anonymous mode", q.identities)
	}
}

func TestIdentifiedModeStoresRawIdentifiers(t *testing.T) {
	q, h := testServerWithIdentity(t, "identified")
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada","$install_id":"i1"},
	  "events":[{"name":"$screen_view","attributes":{"$screen":"/x"}}]}`, nil)

	v := q.views[0]
	if v.UserID != "u1" || v.ActorID != "u1" {
		t.Errorf("view identity = actor %q user %q; want raw u1", v.ActorID, v.UserID)
	}
	if len(q.identities) != 1 || q.identities[0].Name != "Ada" ||
		q.identities[0].Kind != store.KindUser || q.identities[0].Project != "app" {
		t.Errorf("identities = %+v", q.identities)
	}
}

func TestIdentifiedModeFallsBackToInstallThenHash(t *testing.T) {
	q, h := testServerWithIdentity(t, "identified")
	post(h, `{"key":"`+testKey+`","attributes":{"$install_id":"i1"},
	  "events":[{"name":"a"}]}`, nil)
	post(h, envelopeOf(`{"name":"b"}`), nil)

	if q.events[0].ActorID != "i1" {
		t.Errorf("actor with install only = %q, want i1", q.events[0].ActorID)
	}
	if len(q.events[1].ActorID) != 16 {
		t.Errorf("actor with no identifier = %q, want the rotating hash", q.events[1].ActorID)
	}
}

func TestBatchNamesAreDedupedAcrossEvents(t *testing.T) {
	q, h := testServerWithIdentity(t, "identified")
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada","$group_id":"g","$group_name":"G"},
	  "events":[{"name":"a"},{"name":"b"},{"name":"c"}]}`, nil)
	if len(q.identities) != 2 {
		t.Errorf("identities = %+v; want one user and one group, not one pair per event", q.identities)
	}
}

// --- bot filtering ---

func TestBotFilterAppliesOnlyToPageviews(t *testing.T) {
	q, h := testServer(t)
	body := envelopeOf(`{"name":"$pageview","attributes":{"$url":"https://app.com/x"}},
		{"name":"$screen_view","attributes":{"$screen":"/s"}},
		{"name":"custom"}`)
	w := post(h, body, map[string]string{"User-Agent": "Googlebot/2.1"})

	if len(q.hits) != 0 {
		t.Errorf("bot pageview should be dropped, got %+v", q.hits)
	}
	if len(q.views) != 1 || len(q.events) != 1 {
		t.Errorf("bot filter must not touch app or custom events: views=%+v events=%+v", q.views, q.events)
	}
	// Dropped bot hits count as accepted: the client did nothing wrong.
	if res := decodeResult(t, w); res.Accepted != 3 || res.Rejected != 0 {
		t.Errorf("result = %+v", res)
	}
}

// --- removed endpoints, CORS, health ---

func TestOldEndpointsAreGone(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/api/hit", "/api/event"} {
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, w.Code)
		}
	}
}

func TestPreflight(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("OPTIONS", "/api/events", nil)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("preflight allowed origin: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Analytics-Key") {
		t.Errorf("preflight must allow the key header: %q", w.Header().Get("Access-Control-Allow-Headers"))
	}
	r2 := httptest.NewRequest("OPTIONS", "/api/events", nil)
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

func TestKeyCountersRecordPerLabel(t *testing.T) {
	q, h := testServer(t)
	_ = q
	post(h, envelopeOf(`{"name":"a"},{"name":""}`), nil)
	srv, ok := h.(*Server)
	if !ok {
		t.Fatal("handler is not *Server")
	}
	counts := srv.Counters().Drain()
	if got := counts["web"]; got != [2]int{1, 1} {
		t.Errorf("counters[web] = %v, want [1 1]", got)
	}
	if len(srv.Counters().Drain()) != 0 {
		t.Error("Drain must reset the counters")
	}
}
