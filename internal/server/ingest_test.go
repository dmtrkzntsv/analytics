package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMergeAttributesEventOverridesBatch(t *testing.T) {
	batch := map[string]any{"$platform": "ios", "$app_version": "2.4.1", "team": "core"}
	event := map[string]any{"$app_version": "2.5.0", "plan": "pro"}

	got := mergeAttributes(batch, event)

	want := map[string]string{
		"$platform": "ios", "$app_version": "2.5.0", "team": "core", "plan": "pro",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("merged has %d keys, want %d", len(got), len(want))
	}
}

func TestMergeAttributesDoesNotMutateInputs(t *testing.T) {
	batch := map[string]any{"$platform": "ios"}
	event := map[string]any{"$platform": "android"}
	mergeAttributes(batch, event)
	if batch["$platform"] != "ios" || event["$platform"] != "android" {
		t.Errorf("inputs mutated: batch=%v event=%v", batch, event)
	}
}

func TestMergeAttributesHandlesNilMaps(t *testing.T) {
	if got := mergeAttributes(nil, nil); len(got) != 0 {
		t.Errorf("merge(nil,nil) = %v, want empty", got)
	}
	if got := mergeAttributes(nil, map[string]any{"a": "b"}); got["a"] != "b" {
		t.Errorf("merge(nil,event) = %v", got)
	}
}

func TestResolveAttributesSplitsReservedFromCustom(t *testing.T) {
	r, unknown := resolveAttributes(map[string]any{
		"$install_id": "018f", "$user_id": "u1", "$user_name": "Ada",
		"$group_id": "org9", "$group_name": "Acme", "$session_id": "s1",
		"$platform": "ios", "$app_version": "2.4.1", "$os_version": "17.2",
		"$device_model": "iPhone15,2", "$locale": "en-US",
		"$url": "https://x/y", "$referrer": "https://z", "$screen": "/settings",
		"plan": "pro", "count": float64(3), "ok": true, "nothing": nil,
	})

	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}
	if r.InstallID != "018f" || r.UserID != "u1" || r.UserName != "Ada" {
		t.Errorf("identity = %+v", r)
	}
	if r.GroupID != "org9" || r.GroupName != "Acme" || r.SessionID != "s1" {
		t.Errorf("group/session = %+v", r)
	}
	if r.Platform != "ios" || r.AppVersion != "2.4.1" || r.OSVersion != "17.2" {
		t.Errorf("environment = %+v", r)
	}
	if r.DeviceModel != "iPhone15,2" || r.Locale != "en-US" {
		t.Errorf("device = %+v", r)
	}
	if r.URL != "https://x/y" || r.Referrer != "https://z" || r.Screen != "/settings" {
		t.Errorf("payload = %+v", r)
	}
	// float64(3) must not render as "3.000000"; bool and nil round-trip.
	if r.Custom["plan"] != "pro" || r.Custom["count"] != "3" ||
		r.Custom["ok"] != "true" || r.Custom["nothing"] != "" {
		t.Errorf("custom = %v", r.Custom)
	}
	if _, ok := r.Custom["$platform"]; ok {
		t.Error("reserved key leaked into custom attributes")
	}
}

func TestResolveAttributesReportsUnknownReservedKeys(t *testing.T) {
	r, unknown := resolveAttributes(map[string]any{"$app_ver": "2.4.1", "plan": "pro"})

	if len(unknown) != 1 || unknown[0] != "$app_ver" {
		t.Fatalf("unknown = %v, want [$app_ver]", unknown)
	}
	if _, ok := r.Custom["$app_ver"]; ok {
		t.Error("unknown reserved key must be dropped, not stored")
	}
	if r.Custom["plan"] != "pro" {
		t.Error("ordinary attributes must survive an unknown reserved key")
	}
}

func TestResolveAttributesTruncatesLongValues(t *testing.T) {
	long := strings.Repeat("x", maxAttrValue+100)
	r, _ := resolveAttributes(map[string]any{"blob": long, "$user_id": long})
	if len(r.Custom["blob"]) != maxAttrValue {
		t.Errorf("custom value length = %d, want %d", len(r.Custom["blob"]), maxAttrValue)
	}
	if len(r.UserID) != maxAttrValue {
		t.Errorf("reserved value length = %d, want %d", len(r.UserID), maxAttrValue)
	}
}

func TestResolveAttributesDropsOverlongKeys(t *testing.T) {
	r, _ := resolveAttributes(map[string]any{strings.Repeat("k", maxAttrKey+1): "v"})
	if len(r.Custom) != 0 {
		t.Errorf("custom = %v, want the overlong key dropped", r.Custom)
	}
}

func TestResolveAttributesCapsAttributeCount(t *testing.T) {
	in := map[string]any{}
	for i := 0; i < maxAttrs*2; i++ {
		in[string(rune('a'+i%26))+strconv.Itoa(i)] = "v"
	}
	r, _ := resolveAttributes(in)
	if len(r.Custom) != maxAttrs {
		t.Errorf("custom count = %d, want %d", len(r.Custom), maxAttrs)
	}
}

func TestClampTS(t *testing.T) {
	received := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * 24 * time.Hour

	cases := []struct {
		name    string
		client  time.Time
		want    time.Time
		clamped bool
	}{
		{"in range", received.Add(-time.Hour), received.Add(-time.Hour), false},
		{"too old", received.Add(-365 * 24 * time.Hour), received.Add(-maxAge), true},
		{"exactly at the boundary", received.Add(-maxAge), received.Add(-maxAge), false},
		{"too far future", received.Add(time.Hour), received, true},
		{"small future skew allowed", received.Add(2 * time.Minute), received.Add(2 * time.Minute), false},
		{"zero falls back to received", time.Time{}, received, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, clamped := clampTS(c.client, received, maxAge)
			if !got.Equal(c.want) || clamped != c.clamped {
				t.Errorf("clampTS = %v, %v; want %v, %v", got, clamped, c.want, c.clamped)
			}
		})
	}
}

func TestNoticeCaps(t *testing.T) {
	var res ingestResult
	for i := 0; i < maxNotices*3; i++ {
		res.reject(i, "bad %d", i)
		res.warn(i, "odd %d", i)
	}
	if len(res.Errors) != maxNotices || len(res.Warnings) != maxNotices {
		t.Errorf("errors=%d warnings=%d; both want %d", len(res.Errors), len(res.Warnings), maxNotices)
	}
	// Rejections are still counted in full even once notices stop accruing,
	// so the count never understates what was dropped.
	if res.Rejected != maxNotices*3 {
		t.Errorf("Rejected = %d, want %d", res.Rejected, maxNotices*3)
	}
}
