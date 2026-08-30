# twillingate.js — the JS SDK

The collector serves its own SDK at `/js/twillingate.js` (compiled from the
TypeScript in [`sdk/`](../sdk/), embedded in the binary). One file, two
modes: a drop-in snippet for websites, and a full SDK for driving web,
product and app analytics from code. The wire format underneath is always
[`POST /api/events`](ingest-api.md).

The legacy snippet stays at `/js/script.js`, byte-for-byte — deployed
websites never break — but new integrations should use twillingate.js.

## Snippet mode

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

`twillingate key issue -project <alias> -label <label>` mints the key and
prints this snippet ready to paste. Attributes:

| Attribute | Meaning |
| --- | --- |
| `data-key` | The project's ingest key. Required; public by design. |
| `data-identity` | `anonymous` (default) or `identified`. Mirrors the project's server-side mode for visibility; the server enforces the real one regardless. `identified` additionally authorizes persisting a visitor id in localStorage — consent-relevant (ePrivacy, same category as a cookie). |
| `data-user`, `data-group` | Set when the page is rendered already knowing who is looking at it. |
| `data-auto="off"` | Disable automatic pageviews; drive them with `twillingate.page()`. |

Pageviews are automatic, including on `history.pushState` and `popstate`,
so single-page apps need no extra code.

## SDK-only mode

Load the file without `data-key` (it stays dormant), or bundle
`sdk/src/twillingate.ts`, and initialise yourself:

```js
twillingate.init({
  url: "https://twillingate.example.com",  // defaults to the script's origin
  key: "ak_9f3c…",
  identity: "anonymous",       // or "identified"
  autoPageviews: true,         // default false in explicit init
  // app analytics context, sent as batch attributes:
  platform: "web",             // → $platform
  appVersion: "2.4.1",         // → $app_version
  installId: "018f…",          // → $install_id (stable per install)
  user: "u_123",               // optional page-render identity
  group: "org_9",
});
```

## Runtime API

```js
twillingate.page();                            // manual $pageview (deduped per path)
twillingate.screen("/settings");               // app $screen_view
twillingate.track("signup", { plan: "pro" });  // opt-in product event
twillingate.identify("user-123", "org-9");     // set user and group
twillingate.group("org-9");                    // set group alone
twillingate.reset();                           // on logout — see below
twillingate.flush();                           // force-send the queue
```

- `track(name, attrs)` — product event; don't `$`-prefix your names.
- `identify(user, group?)` — persisted, so every later event carries the
  identity. Events already sent stay unattributed: there is no retroactive
  stitching. The user id is only written to localStorage for `identified`
  projects; the group always persists (a group is an organization, not a
  person).
- `reset()` — required on logout. Without it the next person on a shared
  browser inherits the previous user's identity. Clears user, group and
  the visitor id.
- `screen(name, attrs?)` — app analytics; pair with `platform`,
  `appVersion` and `installId` in `init` so actives, versions and screens
  aggregate properly.

## Transport

Events queue briefly (~1s) and flush as one batch — on the timer, once 20
events accumulate, on `flush()`, and on page unload (`pagehide` /
`visibilitychange` via `sendBeacon`; the key travels in the JSON body
because beacons cannot set headers). Every event carries a UUID and a
client timestamp.

A batch that fails to send (network down, 5xx) persists to a bounded
localStorage queue (`twillingate_queue`, 50 batches) and replays on the
next load or when the browser fires `online`. Replays dedupe server-side
by event id and keep their original timestamps. A 4xx response (bad key,
bad payload) drops the batch instead — resending it forever helps nobody.

## Privacy behaviour

- Anonymous projects: no cookies, no localStorage writes.
- Identified projects: a visitor id persists in `twillingate_visitor`;
  identity written by the legacy snippet (`analytics_visitor` /
  `analytics_user` / `analytics_group`) is migrated automatically (copied,
  not moved).
- The SDK is silent on localhost, `file://` URLs and automated browsers.
- Opt a device out: `localStorage.twillingate_ignore = "true"` (the legacy
  `analytics_ignore` is honoured too).

## Migrating a site from script.js

Swap the `src` to `/js/twillingate.js` and rename any
`analytics.track/identify/reset` calls to `twillingate.*` — everything
else (attributes, key, identity mode) is unchanged, and identified-mode
visitors carry over via the storage migration. Sites using Plausible-style
tagged-event classes: [docs/plausible/](plausible/) has a shim that works
with both globals.
