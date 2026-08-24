package sqlite

import (
	"context"
	"sort"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

func TestKnownAttributeKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	evs := []store.ProductEvent{
		{ID: "1", Project: "app", EventName: "e", UserID: "u", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro", "weird key!": "x"}},
		{ID: "2", Project: "app", EventName: "e", UserID: "u", TS: ts("2026-08-10T11:00:00Z"),
			Attributes: map[string]string{"plan": "free", "source": "ads"}},
		// No attributes at all: must not break json_each or add keys.
		{ID: "3", Project: "app", EventName: "e", UserID: "u", TS: ts("2026-08-10T12:00:00Z")},
	}
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	keys, err := db.KnownAttributeKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	want := []string{"plan", "source", "weird key!"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

func TestKnownAttributeKeysEmpty(t *testing.T) {
	keys, err := newTestDB(t).KnownAttributeKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %v, want none", keys)
	}
}

func TestRebuildFlatView(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	evs := []store.ProductEvent{
		{ID: "1", Project: "app", EventName: "e", UserID: "u1", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}},
	}
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	var id, project, event, user, tsCol, plan string
	if err := db.db.QueryRow(
		`SELECT id, project, event_name, actor_id, ts, attr_plan FROM v_events_flat WHERE id='1'`).
		Scan(&id, &project, &event, &user, &tsCol, &plan); err != nil {
		t.Fatal(err)
	}
	if plan != "pro" {
		t.Fatalf("attr_plan = %q", plan)
	}
	if project != "app" || event != "e" || user != "u1" || tsCol != "2026-08-10T10:00:00Z" {
		t.Fatalf("base columns wrong: %q %q %q %q", project, event, user, tsCol)
	}

	// Rebuild with more keys replaces the view.
	if err := db.RebuildFlatView(ctx, []string{"plan", "source"}); err != nil {
		t.Fatal(err)
	}
	var src *string
	if err := db.db.QueryRow(`SELECT attr_source FROM v_events_flat WHERE id='1'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != nil {
		t.Fatalf("missing attr must be NULL, got %v", *src)
	}

	// Rebuilding with no keys leaves a usable base-column view.
	if err := db.RebuildFlatView(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT id FROM v_events_flat WHERE id='1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Query(`SELECT attr_plan FROM v_events_flat`); err == nil {
		t.Error("attr_plan should be gone after rebuilding without it")
	}
}

func TestRebuildFlatViewHostileKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// Injection attempts and collisions must not break the DDL.
	hostile := []string{
		`x"; DROP TABLE product_events; --`,
		"weird key!",
		"weird-key.", // collides with "weird key!" after sanitizing
		"1starts_with_digit",
		"漢字", // sanitizes to empty -> skipped
	}
	if err := db.RebuildFlatView(ctx, hostile); err != nil {
		t.Fatal(err)
	}
	// product_events must still exist (no injection).
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n); err != nil {
		t.Fatalf("table gone — injection succeeded: %v", err)
	}
	cols := flatViewColumns(t, db)
	if !cols["attr_weirdkey"] || !cols["attr_weirdkey_2"] {
		t.Fatalf("collision suffixing failed: %v", cols)
	}
	if !cols["attr_1starts_with_digit"] {
		t.Errorf("digit-leading key not prefixed into a valid identifier: %v", cols)
	}
	if len(cols) != 5+4 {
		t.Errorf("cols = %v, want 5 base + 4 attrs (漢字 skipped)", cols)
	}
}

// A quote in the key must survive into the JSON path as data, not terminate
// the surrounding SQL string literal.
func TestRebuildFlatViewQuotedKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "1", Project: "app", EventName: "e", UserID: "u", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"it's": "apostrophe", `say"hi`: "doublequote"}},
	}); err != nil {
		t.Fatal(err)
	}
	keys, err := db.KnownAttributeKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, keys); err != nil {
		t.Fatal(err)
	}
	var apos, dq string
	if err := db.db.QueryRow(`SELECT attr_its, attr_sayhi FROM v_events_flat WHERE id='1'`).
		Scan(&apos, &dq); err != nil {
		t.Fatal(err)
	}
	if apos != "apostrophe" {
		t.Errorf("attr_its = %q, want apostrophe", apos)
	}
	if dq != "doublequote" {
		t.Errorf("attr_sayhi = %q, want doublequote", dq)
	}
}

// Column order must not depend on map iteration or caller ordering, so the
// view is stable across restarts.
func TestRebuildFlatViewDeterministicOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.RebuildFlatView(ctx, []string{"zeta", "alpha", "mu"}); err != nil {
		t.Fatal(err)
	}
	first := flatViewColumnList(t, db)
	if err := db.RebuildFlatView(ctx, []string{"mu", "zeta", "alpha"}); err != nil {
		t.Fatal(err)
	}
	second := flatViewColumnList(t, db)
	if len(first) != len(second) {
		t.Fatalf("%v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("column order not deterministic: %v vs %v", first, second)
		}
	}
	want := []string{"id", "project", "event_name", "actor_id", "ts", "attr_alpha", "attr_mu", "attr_zeta"}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("columns = %v, want %v", first, want)
		}
	}
}

func flatViewColumnList(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT name FROM pragma_table_info('v_events_flat')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func flatViewColumns(t *testing.T, db *DB) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	for _, c := range flatViewColumnList(t, db) {
		cols[c] = true
	}
	return cols
}
