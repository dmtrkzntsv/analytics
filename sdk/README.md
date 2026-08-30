# twillingate SDK source

TypeScript source of the JS SDK the collector serves at `/js/twillingate.js`.
The build output is **committed** at `internal/server/twillingate.js` and
embedded into the Go binary, so building the collector never needs Node.

```bash
npm ci
npm test           # vitest (jsdom)
npm run typecheck
npm run build      # rewrites ../internal/server/twillingate.js — commit it
```

CI rebuilds the bundle and fails on any diff against the committed file, so
every source change must ship with a fresh `npm run build`.

The user-facing API reference lives in [docs/sdk.md](../docs/sdk.md); the
wire format the SDK speaks is [docs/ingest-api.md](../docs/ingest-api.md).
This bundle is the only client the collector serves; the legacy
`/js/script.js` snippet was removed once every project had migrated.
