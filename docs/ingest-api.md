# Ingest API

Normative wire format for the analytics collector. This document is the
contract: three independently written clients (iOS, Android, desktop) must
behave identically from it alone.

Spec: `docs/superpowers/specs/2026-08-23-app-analytics-design.md`

## 1. Endpoint

```
POST /api/events
```

This is the only ingest endpoint. There is no separate pageview, event or
batch path — a single event is a batch of one.

## 2. Authentication

Every project has one or more **ingest keys**. The key identifies the
project, so **no payload carries a project field**.

| Client | How the key travels |
|---|---|
| Apps, server-side | `X-Analytics-Key: ak_…` header (preferred) |
| Browsers | `"key"` field in the JSON body |

The header wins when both are present. Browsers must use the body because
`navigator.sendBeacon` cannot set custom headers, and beacons are the only
transport that survives page unload.

Keys are **public by design** — they ship inside app binaries and page
source. Their job is revocation and project identification, not secrecy. A
key that is 128 bits of randomness cannot be guessed, which is why an
unknown key gets a plain `401` rather than a silent drop: a misconfigured
integration deserves a real error, and the response leaks nothing.

Multiple keys per project let a website, an iOS app and a desktop app be
retired on their own schedules. Set `"disabled": true` on a key to retire it
without deleting it.

## 3. Envelope

```jsonc
{
  "key": "ak_9f3c…",            // omit when using the header
  "attributes": {                // batch-level defaults, all optional
    "$install_id": "018f1e5a-…",
    "$user_id": "u_123",
    "$user_name": "Ada Lovelace",
    "$group_id": "org_9",
    "$group_name": "Acme Corp",
    "$session_id": "018f1e5b-…",
    "$platform": "ios",
    "$app_version": "2.4.1",
    "$os_version": "17.2",
    "$device_model": "iPhone15,2",
    "$locale": "en-US"
  },
  "events": [
    { "id": "018f1e5c-…", "ts": "2026-08-23T10:00:00Z",
      "name": "$screen_view", "attributes": { "$screen": "/settings" } },
    { "id": "018f1e5d-…", "ts": "2026-08-23T10:00:05Z",
      "name": "subscribed",
      "attributes": { "plan": "pro", "$app_version": "2.5.0" } }
  ]
}
```

An event is `{id, ts, name, attributes}` and nothing else.

## 4. Attribute merge

Batch-level `attributes` are defaults. Per-event `attributes` override them
**key by key**. That is the only merge rule, and it applies to system (`$`)
and ordinary keys alike.

The rule exists for offline queues. A queue flushed after a week may span an
app self-update: per-event override lets a client stamp `$app_version` on
only the events that differ, instead of grouping its queue by context before
flushing. It also lets a client stamp an ordinary attribute
(`"experiment": "variant_b"`) across a whole flush without repeating it.

## 5. Reserved event names

`$` marks system-defined names. The namespace is **open but reserved**.

| `name` | Stored as | Requires |
|---|---|---|
| `$pageview` | web pageview | `$url` |
| `$screen_view` | app screen view | `$screen` |
| anything else | custom event | `name` |

An **unrecognized `$` name is stored as an ordinary custom event** with a
warning, never rejected. That is deliberate: clients update on app-store
timelines while the server updates on the operator's, so they will be out of
step. Rejecting would mean a client shipping a future `$session_start`
against an older server receives a `4xx`, which §9 classifies as a poison
batch to drop — permanent data loss in exactly the window where forward
compatibility matters.

## 6. Reserved attribute keys

| Group | Keys |
|---|---|
| Identity | `$install_id` `$user_id` `$user_name` `$group_id` `$group_name` `$session_id` |
| Environment | `$platform` `$app_version` `$os_version` `$device_model` `$locale` |
| Event payload | `$url` `$referrer` `$screen` |

An **unrecognized `$` key is dropped** with a warning. It is not stored as an
ordinary attribute: keeping it would add a column to the flattened event view
for what is almost always a typo.

For reserved event names, the reserved payload keys are **typed input to
enrichment, not stored attributes**. `$url` is parsed into a path and UTM
tags and then discarded — the full URL, its query string, the client IP and
the User-Agent are never stored. `$screen` populates its own column.

`$platform` should be one of `ios`, `android`, `macos`, `windows`, `linux`.
`$device_model` and `$os_version` are whatever the platform reports; nothing
is parsed out of a User-Agent, which is why an Electron app is not
misclassified as desktop Chrome.

## 7. Identity

Each project runs in one of two modes, set server-side on the project
registry: `analytics project update -alias <alias> -identity identified`
(or the `update_project` MCP tool).

| Mode | `$user_id`, `$install_id` | `$group_id` | `$user_name` |
|---|---|---|---|
| `anonymous` (default) | salted hash, salt rotates at 00:00 UTC | stored raw | **ignored** |
| `identified` | stored as given | stored raw | stored |

The actor an event is attributed to resolves as `$user_id` → `$install_id` →
a server-side hash of the connection. In `anonymous` mode the result is
hashed with a daily-rotating salt, so nothing links across days.

`$group_id` stays raw in both modes: it identifies an organization, not a
natural person, and hashing it would make dashboards unreadable for no real
privacy gain. If your groups are single-person, treat them as personal data.

`$user_name` is ignored entirely in `anonymous` mode. Storing a person's name
against a hash that rotates daily would defeat the anonymisation and
accumulate a fresh row per user per day.

The server is always the enforcement point. A client cannot opt a project
into storing raw identifiers.

## 8. Timestamps and idempotency

**`ts`** is RFC 3339, UTC. Send the client's own clock reading at the moment
the event happened, not at flush time.

The server clamps `ts` to `[received − max_event_age, received + 5 minutes]`
and records the server clock separately. Out-of-range values are **clamped
and counted, never dropped**, so a device with a broken clock still
contributes. `max_event_age` equals the deployment's
`RETENTION_APP_RAW_DAYS` (30 days by default), which is what guarantees a
clamped event can never target a day that has already been rolled up.

**`id`** is a client-generated UUID (v7 recommended, so ids sort by time).
Supplying one is what makes at-least-once delivery safe: the write ignores a
duplicate primary key, so a batch retried after a timeout that actually
succeeded is a no-op. **Omit `id` and a replayed batch double-counts.**

## 9. Responses and retry

```jsonc
202 {
  "accepted": 2,
  "rejected": 0,
  "errors":   [ { "index": 3, "reason": "$pageview requires $url" } ],
  "warnings": [ { "index": 1, "reason": "unknown reserved key $app_ver, ignored" } ]
}
```

`errors` and `warnings` are each capped at 10 entries. Rejection is **per
event, not per batch**: one malformed event increments `rejected` and the
rest of the batch still lands, so a single bad event cannot poison a
500-event replay.

| Code | Meaning | Client action |
|---|---|---|
| 202 | Accepted for processing | Do not retry |
| 400 | Malformed envelope | Drop — poison batch |
| 401 | Unknown or disabled key | Drop |
| 403 | `Origin` present and not allowed | Drop |
| 413 | Body or event count over limit | Split and retry |
| 5xx | Server fault | Retry with backoff |

**Normative: retry only on 5xx and network failure. Any other 4xx is a
poison batch — drop it.** Retrying a 400 forever is the most common way a
hand-written client turns one bug into an outage.

The `202` is returned **before** the write; the pipeline is asynchronous. So
`accepted` counts validation, not persistence, and must not be treated as a
durability receipt.

## 10. Limits

| Limit | Value |
|---|---|
| Body | 256 KB |
| Events per batch | 500 |
| Attributes per event | 50 |
| Attribute key length | 64 characters |
| Attribute value length | 512 characters (truncated, not rejected) |

## 11. Origin and CORS

If an `Origin` header is present it must match the project's
`allowed_origins`; if absent, the request is accepted. Native apps send no
`Origin` and are unaffected.

Electron and Tauri renderers **do** send one, so add their scheme
(`tauri://localhost`, `app://.`, `file://`) to `allowed_origins`.

Preflight is answered with `Access-Control-Allow-Headers: Content-Type,
X-Analytics-Key` and echoes the matched origin. To skip preflight entirely,
send the key in the body with `Content-Type: text/plain`, which makes the
request CORS-simple.

## 12. Batched `$pageview`

A pageview is enriched from the connection that carried it: client IP for
country, User-Agent for browser and OS, `Origin` for the allowlist. Every
`$pageview` in a request is therefore attributed to the client that sent it.

That is correct for a browser batching its own pageviews. **A backend must
not relay pageviews on behalf of other people** — every one of them would be
attributed to the backend's IP and User-Agent. The server cannot detect this;
it is a contract you keep.

Bot filtering applies to `$pageview` only, on the connection's User-Agent. It
never touches app or custom events, so an app whose HTTP library sends a
non-browser User-Agent is never dropped as a crawler.

## 13. Worked offline queue

The shape every client should implement:

**On event**
1. Generate a UUIDv7 `id` and an RFC 3339 `ts` **now**.
2. Append `{id, ts, name, attributes}` to a durable local queue.

**On flush** (app foreground, network available, or a timer)
1. Take up to 500 events from the head of the queue.
2. POST them with the current context as batch-level `attributes`.
3. On `2xx` **or** any `4xx` other than 429: delete those events from the
   queue. A 4xx will never succeed on retry.
4. On `5xx`, 429, or a network error: keep them and retry with exponential
   backoff (for example 1s, 5s, 25s, capped at a few minutes).
5. On `413`: halve the batch size and retry.

**Never**
- Block the UI on a flush.
- Grow the queue without bound — cap it (say 10 000 events) and drop the
  oldest, which keeps recent data when a device is offline for a week.
- Retry a `400` or `401`.
