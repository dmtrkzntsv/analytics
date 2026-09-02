package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---- tools_read.go: table() and checkRange() branches not reached via
// the happy-path *Overview/*Breakdown tests ----

func TestTableTruncatesErrorsAndTimesOut(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()

	// truncation: three rows for blog (2026-08-20, -21 aggregated plus the
	// live hit on -26), capped to 1.
	h.maxRows = 1
	out, err := h.table(ctx, `SELECT day FROM v_web_daily WHERE project=? AND day BETWEEN ? AND ?`,
		"blog", "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("truncation case: %v", err)
	}
	if !out.Truncated || out.Note == "" {
		t.Errorf("out = %+v, want Truncated with a Note", out)
	}
	h.maxRows = 1000

	// plain SQL error, not a timeout: must not carry the "exceeded" wording.
	_, err = h.table(ctx, `SELECT no_such_column FROM v_web_daily WHERE project=?`, "blog")
	if err == nil {
		t.Fatal("bad column did not error")
	}
	if strings.Contains(err.Error(), "exceeded") {
		t.Errorf("plain SQL error worded as a timeout: %v", err)
	}

	// timeout: a runaway recursive query against a host with a tiny deadline.
	h.timeout = 20 * time.Millisecond
	_, err = h.table(ctx,
		`WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r) SELECT COUNT(*) FROM r`)
	if err == nil {
		t.Fatal("runaway query did not time out")
	}
	if !strings.Contains(err.Error(), "exceeded") || !strings.Contains(err.Error(), "narrow the date range") {
		t.Errorf("timeout error not worded as expected: %v", err)
	}
}

func TestCheckRangeRejectsBadDateFormat(t *testing.T) {
	_, cs := newTestHost(t)
	for _, bad := range []map[string]any{
		{"project": "blog", "from": "not-a-date", "to": "2026-08-31"},
		{"project": "blog", "from": "2026-08-01", "to": "8/31/2026"},
	} {
		res := callTool(t, cs, "web_overview", bad)
		if !res.IsError {
			t.Errorf("bad range %v accepted", bad)
		}
	}
}

// ---- tools_manage.go: error branches and the skip_key path ----

func TestCreateProjectSkipKey(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"alias": "nokey", "name": "No Key", "skip_key": true})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if strings.Contains(out, `"key"`) || strings.Contains(out, "script.js") {
		t.Errorf("skip_key:true still returned a key/snippet: %s", out)
	}
	list := callTool(t, cs, "list_ingest_keys", map[string]any{"project": "nokey"})
	if strings.Contains(textOf(list), "default") {
		t.Errorf("skip_key:true still issued a key: %s", textOf(list))
	}
}

func TestCreateProjectToolValidationError(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"alias": "bad", "identity": "sometimes"})
	if !res.IsError {
		t.Fatal("invalid identity accepted")
	}
}

// Project-scoped tools answer an unknown alias with the valid ones listed
// (the recoverable form a model can act on), whichever layer noticed the
// miss: archive learns it from the store, issue_ingest_key from manage.
func TestArchiveToolUnknownAlias(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "archive_project", map[string]any{"alias": "ghost"})
	if !res.IsError {
		t.Fatal("archive of unknown alias accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "valid aliases: blog") {
		t.Errorf("error = %q, want the valid aliases listed", msg)
	}
}

// restore of an unknown alias is a deliberate no-op at the store layer
// (distinguishing "already restored" from "never existed" would need an
// extra existence check the archive path already pays for); the tool
// must therefore succeed rather than error.
func TestRestoreToolUnknownAliasIsANoOp(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "restore_project", map[string]any{"alias": "ghost"}); res.IsError {
		t.Fatalf("restore of unknown alias errored: %s", textOf(res))
	}
}

func TestIssueKeyToolUnknownProject(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project": "ghost", "label": "web"})
	if !res.IsError {
		t.Fatal("issue_ingest_key for unknown project accepted")
	}
	if msg := textOf(res); !strings.Contains(msg, "valid aliases: blog") {
		t.Errorf("error = %q, want the valid aliases listed", msg)
	}
}

func TestDisableEnableKeyToolsUnknownKey(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "disable_ingest_key", map[string]any{
		"project": "blog", "label": "ghost"}); !res.IsError {
		t.Fatal("disable of unknown key accepted")
	}
	if res := callTool(t, cs, "enable_ingest_key", map[string]any{
		"project": "blog", "label": "ghost"}); !res.IsError {
		t.Fatal("enable of unknown key accepted")
	}
}

func TestListKeysFilterAndUnfiltered(t *testing.T) {
	_, cs := newTestHost(t)
	if res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project": "blog", "label": "web"}); res.IsError {
		t.Fatalf("seed key: %s", textOf(res))
	}
	if res := callTool(t, cs, "issue_ingest_key", map[string]any{
		"project": "docs", "label": "web"}); res.IsError {
		t.Fatalf("seed key: %s", textOf(res))
	}

	filtered := textOf(callTool(t, cs, "list_ingest_keys", map[string]any{"project": "blog"}))
	if !strings.Contains(filtered, "blog") || strings.Contains(filtered, "docs") {
		t.Errorf("project filter not applied: %s", filtered)
	}

	all := textOf(callTool(t, cs, "list_ingest_keys", nil))
	if !strings.Contains(all, "blog") || !strings.Contains(all, "docs") {
		t.Errorf("unfiltered list missing a project: %s", all)
	}
}

func TestListKeysPropagatesStoreError(t *testing.T) {
	h, cs := newTestHost(t)
	// manage.Store has no Close; the test reaches the real store's.
	if err := h.ops.St.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, cs, "list_ingest_keys", nil)
	if !res.IsError {
		t.Fatal("list_ingest_keys on a closed store returned no error")
	}
}

// ---- tools_product.go: kind/surface validation and the event filter ----

func TestIdentitiesRejectsBadKind(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project": "blog", "kind": "robot", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("bad kind accepted")
	}
}

func TestIdentitiesGroupKind(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project": "blog", "kind": "group", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "g1") || !strings.Contains(out, "Acme Inc") {
		t.Errorf("group identity missing: %s", out)
	}
}

func TestRetentionRejectsBadSurface(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project": "blog", "surface": "mobile", "from": "2026-07-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("bad surface accepted")
	}
}

func TestProductEventsFiltersByEventName(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_events", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31", "event": "signup"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "signup") {
		t.Errorf("event filter dropped the row: %s", out)
	}
	// a name with no matching rows must succeed with an empty table, not error
	res = callTool(t, cs, "product_events", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31", "event": "no-such-event"})
	if res.IsError {
		t.Fatalf("unmatched event name errored: %s", textOf(res))
	}
}
