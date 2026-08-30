package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/enrich"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil { // only on entropy exhaustion; fall back to v4
		return uuid.NewString()
	}
	return id.String()
}

// handleEvents is the only ingest endpoint. It demultiplexes by event name:
// $pageview to web_hits, $screen_view to app_views, everything else to
// product_events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var env envelope
	if !decode(w, r, &env) {
		return
	}

	// Header preferred; body accepted because navigator.sendBeacon cannot
	// set custom headers, so browsers have no other option.
	key := r.Header.Get("X-Analytics-Key")
	if key == "" {
		key = env.Key
	}
	p, label, ok := s.reg.Snapshot(r.Context()).ProjectByKey(key)
	if !ok {
		// One auth outcome. Because the key resolves the project, there is
		// no unknown-project case to keep indistinguishable from a bad key.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Origin only deters browser-based abuse, since a scripted client can
	// spoof or omit it — which is exactly the case worth keeping. Native
	// apps send none and are unaffected; Electron and Tauri renderers add
	// their scheme to allowed_origins.
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(w, r, p.Alias) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if len(env.Events) > maxBatchEvents {
		http.Error(w, "too many events", http.StatusRequestEntityTooLarge)
		return
	}

	salt, err := s.salt.Current(r.Context())
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	received := time.Now().UTC()
	ip, ua := clientIP(r), r.Header.Get("User-Agent")
	country := s.geo.Country(r, ip)
	// Bot filtering is a web concern and applies to $pageview only. Applying
	// it to app traffic would drop every client whose HTTP library sends a
	// non-browser User-Agent.
	botUA := enrich.IsBot(ua)
	maxAge := s.cfg.MaxEventAge()

	var res ingestResult
	var names []store.Identity
	for i, ev := range env.Events {
		rv, unknown := resolveAttributes(mergeAttributes(env.Attributes, ev.Attributes))
		for _, k := range unknown {
			res.warn(i, "unknown reserved key %s, ignored", k)
		}

		if ev.Name == "" {
			res.reject(i, "name is required")
			continue
		}
		id, err := eventID(ev.ID)
		if err != nil {
			res.reject(i, "%v", err)
			continue
		}
		ts, clamped := clampTS(parseTS(ev.TS), received, maxAge)
		if clamped {
			res.warn(i, "timestamp out of range, clamped")
		}

		actor, user, group := resolveIdentity(p, rv, salt, ip, ua)
		names = append(names, identityNames(p, rv)...)

		switch ev.Name {
		case namePageview:
			if rv.URL == "" {
				res.reject(i, "$pageview requires $url")
				continue
			}
			page, err := enrich.ParsePageURL(rv.URL)
			if err != nil {
				res.reject(i, "invalid $url")
				continue
			}
			if botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			source := enrich.CleanReferrer(rv.Referrer, page.Host)
			if source == "" && page.Ref != "" {
				source = page.Ref
			}
			device, browser, osName := enrich.ParseUserAgent(ua)
			s.queue.EnqueueHit(store.WebHit{
				ID: id, Project: p.Alias, TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group,
				Path: page.Path, ReferrerSource: source,
				UTMSource: page.UTMSource, UTMMedium: page.UTMMedium,
				UTMCampaign: page.UTMCampaign,
				Country:     country, Device: device, Browser: browser, OS: osName,
			})
			res.Accepted++

		case nameScreenView:
			if rv.Screen == "" {
				res.reject(i, "$screen_view requires $screen")
				continue
			}
			s.queue.EnqueueAppView(store.AppView{
				ID: id, Project: p.Alias, TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group, SessionID: rv.SessionID,
				Screen:      rv.Screen,
				Platform:    rv.Platform,
				Version:     rv.Version,
				OSVersion:   rv.OSVersion,
				DeviceModel: rv.DeviceModel,
				Locale:      rv.Locale,
				Country:     country,
			})
			res.Accepted++

		default:
			if strings.HasPrefix(ev.Name, "$") {
				res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
			}
			s.queue.EnqueueEvent(store.ProductEvent{
				ID: id, Project: p.Alias, EventName: ev.Name,
				TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group,
				Platform: rv.Platform, Version: rv.Version,
				Attributes: rv.Custom,
			})
			res.Accepted++
		}
	}

	if names = dedupeIdentities(names, p.Alias); len(names) > 0 {
		if err := s.names.UpsertIdentities(r.Context(), names); err != nil {
			s.logger.Error("identity upsert failed", "error", err)
		}
	}
	s.counters.record(label, res.Accepted, res.Rejected)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(res); err != nil {
		s.logger.Error("write response", "error", err)
	}
}

// resolveIdentity applies the project's identity mode. anonymous salts and
// rotates whatever identifier the client supplied; identified stores it as
// given.
//
// group_id stays raw in both modes: it identifies an organization, not a
// natural person, and hashing it would make dashboards unreadable for no
// real privacy gain.
func resolveIdentity(p *manage.Project, rv resolved, salt, ip, ua string) (actor, user, group string) {
	raw := rv.UserID
	if raw == "" {
		raw = rv.InstallID
	}
	if p.Identity == config.IdentityIdentified {
		actor = raw
		if actor == "" {
			// No client identifier at all: fall back to the rotating hash
			// rather than dropping the event.
			actor = identity.VisitorHash(salt, ip, ua, p.Alias)
		}
		return actor, rv.UserID, rv.GroupID
	}
	if raw == "" {
		actor = identity.VisitorHash(salt, ip, ua, p.Alias)
	} else {
		actor = identity.ActorHash(salt, raw, p.Alias)
	}
	if rv.UserID != "" {
		user = identity.ActorHash(salt, rv.UserID, p.Alias)
	}
	return actor, user, rv.GroupID
}

// identityNames collects display names to upsert.
//
// $user_name is ignored in anonymous mode: storing a person's name against a
// hash that rotates daily would both defeat the anonymisation and accumulate
// a fresh row per user per day. $group_name is kept in both modes, on the
// same reasoning that keeps group_id raw.
func identityNames(p *manage.Project, rv resolved) []store.Identity {
	var out []store.Identity
	if rv.GroupID != "" && rv.GroupName != "" {
		out = append(out, store.Identity{Kind: store.KindGroup, ID: rv.GroupID, Name: rv.GroupName})
	}
	if p.Identity == config.IdentityIdentified && rv.UserID != "" && rv.UserName != "" {
		out = append(out, store.Identity{Kind: store.KindUser, ID: rv.UserID, Name: rv.UserName})
	}
	return out
}

// dedupeIdentities collapses the repeats a batch-level name produces across
// every event, and stamps the project.
func dedupeIdentities(in []store.Identity, project string) []store.Identity {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, i := range in {
		k := i.Kind + "\x00" + i.ID
		if seen[k] {
			continue
		}
		seen[k] = true
		i.Project = project
		out = append(out, i)
	}
	return out
}

// eventID validates a client-supplied UUID or generates one. Supplying an id
// is what makes an at-least-once retry safe: the write is INSERT OR IGNORE
// on this primary key, so a replayed batch is a no-op.
func eventID(id string) (string, error) {
	if id == "" {
		return newID(), nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("id is not a valid UUID")
	}
	return id, nil
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
