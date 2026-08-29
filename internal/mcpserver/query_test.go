package mcpserver

import (
	"strings"
	"testing"
)

func TestQueryToolSelects(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "query", map[string]any{
		"sql": "SELECT project, day, visitors FROM v_web_daily WHERE project='blog' ORDER BY day"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "2026-08-20") {
		t.Errorf("missing data: %s", out)
	}
}

func TestQueryToolBlocksWrites(t *testing.T) {
	_, cs := newTestHost(t)
	for _, q := range []string{
		"DELETE FROM web_hits",
		"INSERT INTO meta (key, value) VALUES ('x','y')",
		"UPDATE projects SET name='pwned'",
		"PRAGMA journal_mode=DELETE",
		"SELECT 1; DELETE FROM web_hits",
		"ATTACH DATABASE '/etc/passwd' AS pwn",
		"attach database '/tmp/x' as pwn",
		"WITH x AS (SELECT 1) ATTACH DATABASE '/tmp/x' AS pwn",
	} {
		res := callTool(t, cs, "query", map[string]any{"sql": q})
		if !res.IsError {
			t.Errorf("accepted: %s", q)
		}
	}
}

func TestQueryToolCapsRows(t *testing.T) {
	h, cs := newTestHost(t)
	h.maxRows = 1
	res := callTool(t, cs, "query", map[string]any{
		"sql": "WITH n(i) AS (VALUES (1),(2),(3)) SELECT i FROM n"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "PARTIAL") {
		t.Errorf("truncation not flagged: %s", out)
	}
}
