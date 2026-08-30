package mcpserver

// Model-facing integration documentation, served as MCP resources.
//
// Provenance: docsEvents is written from internal/server/ingest.go (the
// reservedKeys map and reserved-name constants are the machine-readable
// truth) and docsJSSDK from sdk/src/twillingate.ts (the shipped SDK).
// docs_sync_test.go binds the load-bearing facts in both to those source
// files: change the code without the doc and the tests go red. The wire
// format itself is not duplicated here — docs://ingest-api embeds
// docs/ingest-api.md verbatim.

// docsEvents is the semantic model of the three event families.
const docsEvents = `# Event model

Everything goes to one endpoint, POST /api/events. The event NAME decides
which family it lands in, and each family feeds a different surface:

| name        | family  | feeds |
|-------------|---------|-------|
| $pageview   | web     | web dashboards (visitors, pages, referrers, countries, devices, UTM) and the web_* MCP tools |
| $screen_view| app     | app dashboards (actives, screens, versions) and the app_* MCP tools |
| anything else | product | product tools (product_events, product_attributes) |

The $ prefix is reserved for the system. An unrecognized $-name is stored
as an ordinary custom event (forward compatibility); an unrecognized
$-attribute key is DROPPED, with a warning in the response body.

## Web ($pageview)

Usually automatic: the JS snippet sends pageviews on load and on SPA
navigations (history.pushState / popstate, and hashchange in hash-routing
mode). Only send $pageview manually from a non-browser client rendering
web-like content.

A pageview carries its location already split: $path (REQUIRED) and $host,
stored verbatim -- the server does no URL parsing, no normalization, no
case folding. The client owns normalization, which is what lets a site
report /account/[id]/edit and keep the raw identifier in the browser.
$path may contain a '#' (hash routing) or a '?' (opt-in query routing).

Campaign parameters travel explicitly as $utm_source, $utm_medium and
$utm_campaign. $referrer is reduced to a source name, and is suppressed as
a self-referral when its host matches $host. Bots are filtered on
User-Agent for pageviews only.

## App ($screen_view)

Sent by native/desktop apps over the HTTP API. Context travels as reserved
attributes: $install_id (the app-install identifier — under anonymous
identity it is salted+rotated daily, more accurate than IP hashing),
$screen, $session_id (optional; without it sessions are inferred from
30-minute gaps), $platform, $app_version, $os_version, $device_model,
$locale. No User-Agent parsing ever happens for apps — they declare their
context.

## Product (custom events)

Any other name: "signup", "subscribed", "export_finished". Keep names
stable and lowercase; they are the aggregation key. Non-$ attributes are
yours (up to 50 per event, keys <= 64 chars, values <= 512 chars).

Product events are counted per day out of the box (count + unique users
per event name, plus daily totals) whether or not any attributes are
declared. ATTRIBUTE BREAKDOWNS need a project to declare which keys are
worth reporting on:

    {"attributes": ["plan", "tier"]}

- attributes is a flat list of custom keys; each declared key gets a value
  breakdown in product_attributes (and its own attr_* column in
  v_events_flat). Undeclared keys are still stored, just not broken down.
- PRODUCT_ATTRIBUTES_TOP_N (server-wide, default 50) caps distinct values
  kept per key; the rest collapse into an "(other)" row with a true
  unique-user count.
- Set attributes with the update_project tool or ` + "`twillingate project create/update -attr`" + `.
  $platform and $app_version break down automatically without being
  declared — do not add them to attributes, $-prefixed keys never reach
  the custom attribute blob.

## Identity across all families

$user_id / $user_name / $group_id / $group_name attach events to users and
groups. Under identity=anonymous, $user_name is ignored entirely and actor
ids rotate daily (no retention curves possible). Under identity=identified,
ids and names are stored as given — a privacy-significant, consent-relevant
setting. $group_id/$group_name are stored raw in both modes.

## Reserved attribute keys (complete list)

$install_id, $user_id, $user_name, $group_id, $group_name, $session_id,
$platform, $app_version, $os_version, $device_model, $locale, $host,
$path, $utm_source, $utm_medium, $utm_campaign, $referrer, $screen.

Wire-format details (batching, retries, offline replay, timestamps,
idempotency): read docs://ingest-api.`

// docsJSSDK documents the shipped SDK (sdk/src/twillingate.ts, served as
// /js/twillingate.js).
const docsJSSDK = `# JS SDK (twillingate.js)

The collector serves the SDK itself at /js/twillingate.js. Two modes:

## Snippet mode (websites)

    <script defer src="https://YOUR-COLLECTOR/js/twillingate.js"
            data-key="ak_..."
            data-identity="anonymous"></script>

(create_project / issue_ingest_key return this snippet with the real key
and collector URL filled in.)

- data-key (required): the project's ingest key. Public by design.
- data-identity: "anonymous" (default) or "identified". This mirrors the
  project's server-side setting for visibility; the SERVER enforces the
  real mode regardless. "identified" additionally authorizes the SDK
  to persist a visitor id in localStorage — which is consent-relevant
  (ePrivacy: same category as a cookie). Gate the tag on consent.
- data-user, data-group: set when the page is rendered already knowing who
  is looking at it.
- data-auto="off": disable automatic pageviews (drive them via page()).

Pageviews are automatic on load AND on SPA navigations (history.pushState
and popstate are hooked) — single-page apps need no extra code.

## SDK-only mode (web, product and app analytics from code)

Load the file WITHOUT data-key (or bundle sdk/src/twillingate.ts) and
initialise yourself:

    twillingate.init({
      url: "https://YOUR-COLLECTOR", key: "ak_...",
      identity: "anonymous",          // or "identified"
      autoPageviews: true,            // off by default in explicit init
      platform: "web",                // app analytics batch context:
      appVersion: "2.4.1",            // $platform / $app_version /
      installId: "<stable-uuid>",     // $install_id
    });

## Runtime API (window.twillingate)

    twillingate.page();                            // $pageview for the current page (deduped)
    twillingate.page("/settings");                 // $pageview for an explicit path
    twillingate.page(fn);                          // register a pageview listener (see below)
    twillingate.screen("/settings");               // app $screen_view
    twillingate.track("signup", { plan: "pro" });  // custom product event
    twillingate.attrs({ tier: "beta" });        // default attributes on every event
    twillingate.identify("user-123", "Ada");       // $user_id + optional $user_name
    twillingate.group("org-9", "Acme");            // $group_id + optional $group_name
    twillingate.reset();                           // on logout
    twillingate.flush();                           // force-send the queue

- track(name, attrs): sends a product event. Do not $-prefix your names.
- attrs(attrs): defaults merged under every event's own attributes
  (event attributes win); successive calls merge, attrs(null) clears.
- page(fn): the listener runs for every pageview, automatic ones included,
  receiving {url, path, referrer, attributes}; return an object to merge
  extra attributes, false to cancel the pageview.
- identify(userId, userName?): attaches subsequent events to the user.
  There is no retroactive stitching: events sent before identify() keep no
  user id. The name is only stored for identified projects.
- group(groupId, groupName?): attaches subsequent events to the group.
- reset(): call on logout, or the next person on a shared browser inherits
  the previous user's identity.

## Behaviour

- Events batch briefly and flush on a short timer, on page unload
  (sendBeacon; the key travels in the JSON body because beacons cannot set
  headers) and on flush(). Failed batches persist to a bounded
  localStorage queue and replay on the next load or when the browser comes
  back online; ids and client timestamps make replays dedupe server-side.
- The SDK is silent on localhost, file:// URLs and automated browsers.
- Opt out a device with: localStorage.twillingate_ignore = "true" (the
  legacy analytics_ignore is honoured too).
- Identified-mode identity persisted by the legacy snippet (analytics_*
  localStorage keys) is migrated automatically.

## Helpers

The collector serves optional helper scripts next to the SDK:

- /js/plausible-shim.js — fires events from Plausible's plausible-event-*
  CSS classes, so a site migrating off Plausible keeps its tagged CTAs
  with no markup changes. Load it AFTER the tracking snippet. Works with
  window.twillingate and the legacy window.analytics.

## Legacy snippet

/js/script.js has been removed and returns 404. Sites still carrying that
tag must swap the src to /js/twillingate.js and rename window.analytics
calls to twillingate.*; identified-mode visitors carry over via the
analytics_* storage migration.

Server-side and native clients can skip the SDK entirely and POST to
/api/events — read docs://ingest-api.`
