# Connecting an MCP client

`serve` exposes one MCP endpoint — streamable HTTP at
`https://twillingate.example.com/mcp`. There is no stdio server to
install: every client talks to the running collector over the network.

What the client has to prove is decided by the `MCP_AUTH_DSN` mode you
picked in [mcp-auth.md](mcp-auth.md), and that is the whole difference
between the recipes below:

| Mode | Claude Code | Claude Desktop | claude.ai |
| --- | --- | --- | --- |
| `token://` | ✅ `--header` | ⚠️ needs the `mcp-remote` bridge | ❌ cannot send custom headers |
| `cloudflare://` | ✅ browser login | ✅ custom connector | ✅ custom connector |
| `oauth://` | ✅ browser login | ✅ custom connector | ✅ if the IdP does Dynamic Client Registration |

A connected session reads every non-archived project and can use the
management tools; there is no per-project scoping. Treat the credential
as an admin credential.

## Claude Code

### `token://`

```bash
claude mcp add --transport http twillingate https://twillingate.example.com/mcp \
  --header "Authorization: Bearer ar_…"
```

The default scope is `local` — this machine, this project only. Add
`-s user` to get it in every project you open, or `-s project` to write a
checked-in `.mcp.json` for the team. **Do not put the token in a
`-s project` config**: `.mcp.json` is committed. Reference an environment
variable instead, which Claude Code expands at connect time:

```json
{
  "mcpServers": {
    "twillingate": {
      "type": "http",
      "url": "https://twillingate.example.com/mcp",
      "headers": { "Authorization": "Bearer ${TWILLINGATE_MCP_TOKEN}" }
    }
  }
}
```

### `cloudflare://` and `oauth://`

Add the server without a header, then authenticate interactively:

```bash
claude mcp add --transport http twillingate https://twillingate.example.com/mcp
```

Run `/mcp` inside Claude Code, pick `twillingate`, choose *Authenticate*.
A browser opens for the Cloudflare Access or IdP login; Claude Code keeps
the resulting tokens and refreshes them. Re-run `/mcp` → *Clear
authentication* if you need to log in as somebody else.

### Verifying

```bash
claude mcp list          # → twillingate: … - ✓ Connected
```

Inside a session, `/mcp` lists the server and its tools; they appear to
the model as `mcp__twillingate__web_overview` and friends. After rotating
a token, `claude mcp remove twillingate` and add it again — the old
header is cached in the config, not re-read from your shell.

## Claude Desktop

### `cloudflare://` and `oauth://` — custom connector

Settings → Connectors → **Add custom connector**, name it `twillingate`
and give it the endpoint URL `https://twillingate.example.com/mcp`.
Desktop discovers the authorization server, registers itself and opens
the login in a browser. This is the path Cloudflare's managed OAuth and
Dynamic Client Registration exist for — no client id to configure.

### `token://` — the `mcp-remote` bridge

The connector UI cannot attach an `Authorization` header, so a static
token needs a local stdio-to-HTTP bridge. It needs Node installed.

Edit the config file:

- macOS — `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows — `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "twillingate": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote",
        "https://twillingate.example.com/mcp",
        "--header", "Authorization: Bearer ar_…"
      ]
    }
  }
}
```

Then quit Claude Desktop completely — closing the window leaves it
running in the tray, and the config is only read at startup. The server
shows up under the tools icon in the composer.

Note that the token is stored in plain text in a file the desktop app
reads, and `mcp-remote` is third-party code in the path of an admin
credential. If that is not acceptable, put the MCP hostname behind
Cloudflare Access and use the connector instead.

## claude.ai

Same custom-connector flow as Desktop, and the same constraint: it only
works in `cloudflare://` or `oauth://` mode, because the browser client
cannot send a custom header. See
[mcp-auth.md](mcp-auth.md#cloudflare--access-managed-oauth) for the
Access application setup that makes this work with no client
configuration at all.

## Other clients

Anything that speaks streamable HTTP MCP (Cursor, Zed, VS Code, custom
SDK clients) connects to the same URL. In `token://` mode send
`Authorization: Bearer <token>`; in the OAuth modes the client must
handle the 401 challenge and the authorization-code flow itself, or use
`mcp-remote` as above.

## What you get once connected

Nineteen tools: `web_overview`, `web_breakdown`, `app_overview`,
`app_breakdown`, `product_events`, `product_attributes`, `retention`,
`identities`, a guarded read-only SQL `query`, project and ingest-key
management (`create_project`, `update_project`, `archive_project`,
`restore_project`, `issue_ingest_key`, `list_ingest_keys`,
`enable_ingest_key`, `disable_ingest_key`, `list_projects`), and
`integration_guide`, which returns paste-ready setup for a given project
and platform. Resources `docs://events`, `docs://js-sdk`,
`docs://ingest-api`, `schema://views` and `schema://projects` give the
model the event model and the queryable schema.

Ask in plain language — "how many visitors did myapp get last week by
country", "which screens do people hit before they subscribe", "issue a
key for the marketing site".

## Troubleshooting

**`401` on connect.** Check the endpoint directly first, with the
commands in [mcp-auth.md](mcp-auth.md#verifying-any-mode). If curl gets a
`401` too, the problem is the server or the credential, not the client.

**Connects but no tools.** You are on the wrong path — the endpoint is
`/mcp`, not the bare hostname.

**Desktop shows nothing after editing the config.** JSON syntax error, or
the app was never fully quit. Node also has to be on the `PATH` that the
GUI app inherits, which on macOS is not your shell's `PATH`; an absolute
path to `npx` in `"command"` settles it.

**Login loops in `oauth://` mode.** The IdP is probably issuing tokens
without the expected `aud`. The requirements checklist is in
[mcp-auth.md](mcp-auth.md#oauth--any-standards-compliant-idp).

**Stale OAuth state.** `mcp-remote` caches under `~/.mcp-auth`; delete it
to force a fresh login.
