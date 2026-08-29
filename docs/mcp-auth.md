# MCP authentication

`analytics serve -mcp` refuses to run unauthenticated: `MCP_AUTH_MODE` must
name one of three modes. This page is the operator's guide to each. All
variables live in `/etc/analytics/analytics.env` (or the compose `.env`).

| Mode | Pick it when | Works with |
| --- | --- | --- |
| `token` | One operator, no identity provider — the simplest thing that is secure | Claude Code, Cursor, anything that can send a header. **Not** claude.ai connectors (they cannot send custom headers) |
| `cloudflare` | Your domain is already on Cloudflare | Everything, including claude.ai — Access runs the login and the client self-registers |
| `oauth` | You run or rent an identity provider (Keycloak, Auth0, Authentik, …) | Everything, if the IdP meets the requirements below |

Whatever the mode, put the MCP surface on its own hostname when you can
(`MCP_ADDR` + a second DNS name): `/api/events` and `/js/script.js` must
stay publicly reachable for ingestion, and a dedicated hostname keeps the
access-control story simple.

## `token` — a single static token

```bash
analytics keygen -mcp          # prints: MCP_TOKEN=ar_…
```

```bash
# analytics.env
MCP_AUTH_MODE=token
MCP_TOKEN=ar_…
```

Connect Claude Code:

```bash
claude mcp add --transport http analytics https://analytics.example.com/mcp \
  --header "Authorization: Bearer ar_…"
```

The token is a true secret — unlike ingest keys it reads every project and
authorizes the management tools. Rotate by minting a new one and restarting.

Optionally also set `MCP_AUTH_ISSUER` + `MCP_RESOURCE_URL`: the server then
serves the RFC 9728 discovery document, so clients can find the OAuth path
the day you switch modes. Setting the issuer *requires* setting
`MCP_RESOURCE_URL`.

## `cloudflare` — Access managed OAuth

Cloudflare Access acts as the OAuth authorization server ("managed OAuth",
GA March 2026): it serves the discovery documents at the edge, runs the
browser login against your chosen identity source, and supports Dynamic
Client Registration — which is what lets claude.ai connect with zero
client setup. Your only identity infrastructure is the Cloudflare account.

1. **Route the MCP hostname through Cloudflare** — orange-cloud DNS or a
   `cloudflared` tunnel to the machine running `serve -mcp`.
2. **Create the Access application**: Zero Trust → Access controls →
   Applications → add a **self-hosted** application for the MCP hostname
   (or hostname + path). Scope it so the ingest endpoints are NOT behind
   it.
3. **Add an Allow policy** — your email via Google/GitHub login, a
   one-time PIN, or any configured IdP. This is your user store.
4. **Enable Managed OAuth**: the application's **Advanced settings** →
   turn on **Managed OAuth**, and enable Dynamic Client Registration
   (allow-any, or list your client's callback in `allowed_uris`). It is
   opt-in for self-hosted applications.
5. **Copy the application's AUD tag** from the application overview.
6. **Configure the server**:

```bash
# analytics.env
MCP_AUTH_MODE=cloudflare
MCP_CF_TEAM_DOMAIN=yourteam.cloudflareaccess.com
MCP_CF_AUD=<the application AUD tag>
```

How it works, and why the server still verifies: the token a client holds
under managed OAuth is opaque and validated at the edge. What reaches the
origin is the resolved identity as a JWT in the `Cf-Access-Jwt-Assertion`
header; the server validates it against the team's public keys
(`https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`) with the AUD
tag as the audience. A request that reaches the listener without having
passed Access carries no valid assertion and is rejected — so an exposed
origin port does not bypass Access. In this mode the binary deliberately
serves no discovery document and sends no challenge header; the edge owns
both.

## `oauth` — any standards-compliant IdP

The server is an OAuth 2.1 **resource server** only: it validates the JWTs
your IdP issues, and never sees a password or runs a login page.

```bash
# analytics.env
MCP_AUTH_MODE=oauth
MCP_AUTH_ISSUER=https://auth.example.com          # your IdP's issuer URL
MCP_RESOURCE_URL=https://analytics.example.com/mcp # this server's canonical URL
#MCP_AUTH_AUDIENCE=…   # only if your IdP's aud differs from MCP_RESOURCE_URL
```

What the IdP must provide — verify each before pointing the server at it:

1. **RFC 8414 metadata** at
   `<issuer>/.well-known/oauth-authorization-server`, containing a
   `jwks_uri`. The server fetches this once at startup and refuses to boot
   if it is missing — check first:

   ```bash
   curl -s https://auth.example.com/.well-known/oauth-authorization-server | jq .jwks_uri
   ```

   Many IdPs publish only OIDC discovery
   (`/.well-known/openid-configuration`); most current Keycloak, Auth0 and
   Authentik releases serve the RFC 8414 path too, but confirm with the
   curl above rather than assuming.
2. **Asymmetrically signed access tokens** (RS/ES/PS families). HMAC and
   `alg=none` are rejected. If your IdP issues *opaque* access tokens by
   default, configure it to issue JWTs for this audience.
3. **The audience claim**: tokens must carry `aud` containing
   `MCP_RESOURCE_URL` (or `MCP_AUTH_AUDIENCE`). In most IdPs this means
   registering the MCP server as an API/resource with that identifier and
   having clients request it.
4. **For claude.ai**: Dynamic Client Registration (RFC 7591), or manually
   register the client and configure its id in the connector.

Key rotation is handled: an unknown `kid` triggers a JWKS refetch,
throttled to once a minute.

## Verifying any mode

```bash
curl -si https://analytics.example.com/mcp -X POST | head -3
# → HTTP/1.1 401; in oauth (and issuer-configured token) mode the
#   WWW-Authenticate header names the discovery document

curl -s https://analytics.example.com/.well-known/oauth-protected-resource
# → oauth mode: JSON naming your issuer; cloudflare mode: 404 (the edge
#   serves discovery); token mode without issuer: 404

curl -s https://analytics.example.com/healthz
# → {"status":"ok"} — health stays unauthenticated in every mode
```

A valid session in any mode reads every non-archived project — including
personal data on `identified` projects — and can use the management tools.
There is no per-project scoping; the credential (or the IdP behind it) is
the whole of the access control. See the README's
[Privacy and GDPR](../README.md#privacy-and-gdpr) section before enabling
`-mcp` on an instance with identified projects.
