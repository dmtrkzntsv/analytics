# Migrating from Plausible

Plausible's tagged-events build fires goals from `plausible-event-*` CSS
classes on the markup. This tracker has no class-based binding: it
auto-tracks pageviews and exposes `twillingate.track()` (or `window.analytics.track()` from the
legacy snippet), nothing more.
Swapping the script therefore silently stops every tagged CTA — the classes
stay in the markup and simply nothing reads them.

[`plausible-shim.js`](plausible-shim.js) reads them. The collector serves
it, so installing it is one more `<script>` tag and no markup changes:
every existing `plausible-event-name` class keeps converting, under the
same event name it had in Plausible, so historical goal names still line
up.

---

## 1. Install

The collector serves the shim at `/js/plausible-shim.js`, next to the
tracker itself, so there is nothing to copy. Load it **after** the tracking
snippet, which is what defines the tracker global:

```html
<script defer src="https://a.example.com/js/twillingate.js" data-key="ak_9f3c…"></script>
<script defer src="https://a.example.com/js/plausible-shim.js"></script>
```

Both are `defer`, so they execute in document order. The shim also tolerates
a missing tracker — if the tracker global is absent (blocked, failed to
load) clicks are ignored rather than throwing.

A site that serves every script from its own origin — a strict CSP, or a
bundler that wants the source — can still copy
[`plausible-shim.js`](plausible-shim.js) into its static assets and point
the `src` there. It is standalone, ~80 lines, no build step. The hosted
copy is that same file, embedded in the binary, so the two never diverge.

---

## 2. Class syntax

| Class | Fires |
|---|---|
| `plausible-event-name=Signup` | `track("Signup")` |
| `plausible-event-name--signup_cloud` | `track("signup_cloud")` |
| `plausible-event-name=Download+Report` | `track("Download Report")` |
| `plausible-event-name--signin plausible-event-plan--pro` | `track("signin", {plan: "pro"})` |
| `plausible-event-method=HTTP` (no name) | nothing |

Both separators Plausible documents are accepted: `=` is the primary form
and `--` the one for site builders that strip `=` from class names. The
first `=` or `--` in the class ends the key, so single hyphens inside a name
or value survive. `+` decodes to a space, since a class name cannot contain
one.

A tag on an ancestor counts: clicking an icon inside a tagged button fires
the button's event. A tagged `<form>` fires on submit and everything else on
click, so a tagged form wrapping a tagged button does not count twice.
Middle-clicks fire too — they open links.

Keys beginning with `$` are refused by the shim. They are reserved
attributes (§6 of [ingest-api.md](../ingest-api.md)) and an unrecognized one
is dropped server-side, so failing at the source beats losing it in transit.

---

## 3. Where the events land

Any name that is not `$pageview` or `$screen_view` is stored as a custom
event, so every shimmed CTA becomes a **product event**:

```
click → twillingate.track("signup_cloud")
      → POST /api/events
      → product_events
      → v_product_daily / v_product_totals
      → the Product dashboard page
```

Class props are stored in `product_events.attributes` and broken down per
key and value by `v_product_attrs` — but only for the keys the project
declares in `attributes`. Undeclared props are still stored and still
queryable through the raw `attributes` column, just not broken down for the
dashboard.

---

## 4. What does not carry over

**Per-day uniques only.** In anonymous mode the actor id is a salted hash
that rotates daily. `unique_users` on a single day is sound; the same person
clicking a CTA on Monday and converting on Tuesday counts as two actors, so
a multi-day funnel needs identified mode.

**No page context.** Product events keep `platform`, `app_version` and
attributes. Path, referrer, country, device and browser are enriched only on
the pageview path, and a `$url` attached to a custom event is accepted but
discarded. A site-wide CTA would otherwise be indistinguishable from page to
page, so the shim stamps `path: location.pathname` on every event it fires
(pass an explicit `plausible-event-path--…` class to override it). Drop that
line if a CTA appears on enough distinct URLs to make the breakdown noisy —
the server-wide `PRODUCT_ATTRIBUTES_TOP_N` setting caps what reaches the
dashboard, but the raw rows keep everything.

**No navigation delay.** Plausible's script holds a link click for ~150 ms
so the request escapes before unload. The tracker sends through
`navigator.sendBeacon` with a `keepalive` fetch fallback, both of which
survive unload, so the shim never calls `preventDefault` and clicks stay
instant.

**Outbound links and file downloads** are separate Plausible script
variants, not classes, and the shim does not replace them. Tag those links
by hand if they matter.

---

## 5. Verifying

Find what the markup actually tags before trusting the coverage:

```bash
grep -roh 'plausible-event-[^" ]*' path/to/site | sort -u
```

Every `plausible-event-name…` in that list should appear on the Product page
within a flush interval of clicking it. `POST /api/events` returns the
accept/reject counts per batch, so a CTA that fires but never lands is
visible in the response rather than only in the dashboard.
