package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Wire limits.
const (
	maxBatchEvents = 500
	maxAttrs       = 50
	maxAttrKey     = 64
	maxAttrValue   = 512
	maxNotices     = 10
	futureSkew     = 5 * time.Minute
)

// Reserved event names. The namespace is open but reserved: `$` belongs to
// the server, but an unrecognized `$` name is stored as an ordinary custom
// event rather than rejected. Rejecting would mean a client shipping a
// future reserved name against a not-yet-upgraded server receives a 4xx,
// which clients treat as a poison batch to drop — permanent data loss in
// exactly the window forward compatibility matters.
const (
	namePageview   = "$pageview"
	nameScreenView = "$screen_view"
)

type envelope struct {
	Key        string         `json:"key"`
	Attributes map[string]any `json:"attributes"`
	Events     []rawEvent     `json:"events"`
}

type rawEvent struct {
	ID         string         `json:"id"`
	TS         string         `json:"ts"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

type notice struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type ingestResult struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []notice `json:"errors"`
	Warnings []notice `json:"warnings"`
}

// reject records a per-event failure. Rejection is per event, never per
// batch: one malformed event must not poison a 500-event offline replay.
func (res *ingestResult) reject(i int, format string, a ...any) {
	res.Rejected++
	if len(res.Errors) < maxNotices {
		res.Errors = append(res.Errors, notice{Index: i, Reason: fmt.Sprintf(format, a...)})
	}
}

// warn records something ignored rather than refused. This is what keeps
// "ignore unknown $" from meaning "silently discard": during integration the
// mistake shows up in the response body.
func (res *ingestResult) warn(i int, format string, a ...any) {
	if len(res.Warnings) < maxNotices {
		res.Warnings = append(res.Warnings, notice{Index: i, Reason: fmt.Sprintf(format, a...)})
	}
}

// resolved is the reserved half of an event's attributes split into typed
// fields, plus whatever ordinary attributes remain.
type resolved struct {
	InstallID, UserID, UserName    string
	GroupID, GroupName, SessionID  string
	Platform, AppVersion           string
	OSVersion, DeviceModel, Locale string
	Host, Path, Referrer, Screen   string
	UTMSource, UTMMedium           string
	UTMCampaign                    string
	Custom                         map[string]string
}

// reservedKeys maps every system-defined attribute key to its destination.
// Location attributes are stored verbatim: the client owns normalization
// (masking, routing mode), so the server does no URL parsing at all. That
// is what lets a site report /account/[id]/edit without the raw path ever
// leaving the browser. $screen populates its own column.
var reservedKeys = map[string]func(*resolved, string){
	"$install_id":   func(r *resolved, v string) { r.InstallID = v },
	"$user_id":      func(r *resolved, v string) { r.UserID = v },
	"$user_name":    func(r *resolved, v string) { r.UserName = v },
	"$group_id":     func(r *resolved, v string) { r.GroupID = v },
	"$group_name":   func(r *resolved, v string) { r.GroupName = v },
	"$session_id":   func(r *resolved, v string) { r.SessionID = v },
	"$platform":     func(r *resolved, v string) { r.Platform = v },
	"$app_version":  func(r *resolved, v string) { r.AppVersion = v },
	"$os_version":   func(r *resolved, v string) { r.OSVersion = v },
	"$device_model": func(r *resolved, v string) { r.DeviceModel = v },
	"$locale":       func(r *resolved, v string) { r.Locale = v },
	"$host":         func(r *resolved, v string) { r.Host = v },
	"$path":         func(r *resolved, v string) { r.Path = v },
	"$utm_source":   func(r *resolved, v string) { r.UTMSource = v },
	"$utm_medium":   func(r *resolved, v string) { r.UTMMedium = v },
	"$utm_campaign": func(r *resolved, v string) { r.UTMCampaign = v },
	"$referrer":     func(r *resolved, v string) { r.Referrer = v },
	"$screen":       func(r *resolved, v string) { r.Screen = v },
}

// mergeAttributes layers per-event attributes over batch defaults, key by
// key. This is the only merge rule, and it applies to system and ordinary
// keys alike — which is what lets an offline queue spanning an app
// self-update stamp $app_version on just the events that differ, instead of
// grouping the queue by context before flushing.
//
// Neither input is mutated: batch defaults are reused across every event.
func mergeAttributes(batch, event map[string]any) map[string]any {
	out := make(map[string]any, len(batch)+len(event))
	for k, v := range batch {
		out[k] = v
	}
	for k, v := range event {
		out[k] = v
	}
	return out
}

// resolveAttributes splits merged attributes into typed reserved fields and
// ordinary attributes, returning the unknown `$` keys it dropped. Unknown
// reserved keys are dropped rather than stored: the `$` namespace is
// reserved for system fields, so a `$` key this server does not recognize
// is a client bug, and silently storing it as data would hide that.
func resolveAttributes(m map[string]any) (resolved, []string) {
	r := resolved{Custom: map[string]string{}}
	var unknown []string
	for k, v := range m {
		if strings.HasPrefix(k, "$") {
			set, ok := reservedKeys[k]
			if !ok {
				unknown = append(unknown, k)
				continue
			}
			set(&r, truncate(stringify(v), maxAttrValue))
			continue
		}
		if len(k) > maxAttrKey || len(r.Custom) >= maxAttrs {
			continue
		}
		r.Custom[k] = truncate(stringify(v), maxAttrValue)
	}
	return r, unknown
}

// stringify renders a JSON scalar the way the attributes blob stores it.
// Numbers decode as float64, so integral values must print without a
// decimal point or "3" would become "3.000000".
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// clampTS bounds a client timestamp to [received-maxAge, received+skew].
// Out-of-range values are clamped and counted, never dropped: a device with
// a broken clock still contributes. The lower bound is tied to the app raw
// window, which is what guarantees a clamped event can never target a day
// that has already been aggregated and had its raw rows deleted.
func clampTS(client, received time.Time, maxAge time.Duration) (time.Time, bool) {
	if client.IsZero() {
		return received, false
	}
	if oldest := received.Add(-maxAge); client.Before(oldest) {
		return oldest, true
	}
	if client.After(received.Add(futureSkew)) {
		return received, true
	}
	return client, false
}
