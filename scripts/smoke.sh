#!/usr/bin/env bash
# End-to-end smoke test: boot the real binary on a scratch database, send a
# pageview, an app screen view and a custom event through the single ingest
# endpoint, and verify all three rows land.
set -euo pipefail
cd "$(dirname "$0")/.."

port="${SMOKE_PORT:-18080}"
base="http://127.0.0.1:$port"
dir="$(mktemp -d)"
pid=""
cleanup() {
  [ -n "$pid" ] && kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null
  rm -rf "$dir"
}
trap cleanup EXIT

fail() { echo "SMOKE FAIL: $*" >&2; sed 's/^/  server: /' "$dir/log" >&2 || true; exit 1; }

key="ak_smoke0000000000000000000000000"
cat > "$dir/projects.json" <<EOF
[{ "alias": "dev", "name": "Smoke", "identity": "anonymous",
   "ingest_keys": [{ "key": "$key", "label": "smoke" }],
   "allowed_origins": ["http://localhost"] }]
EOF

env LISTEN_ADDR="127.0.0.1:$port" \
    DATABASE_URL="sqlite://$dir/smoke.db" \
    GEO_URL="none://" \
    LOG_LEVEL=debug LOG_FORMAT=text \
    BUFFER_FLUSH_INTERVAL=200ms \
    PROJECTS_FILE="$dir/projects.json" \
    ./analytics serve > "$dir/log" 2>&1 &
pid=$!

for _ in $(seq 1 50); do
  curl -fs "$base/healthz" > /dev/null 2>&1 && break
  kill -0 "$pid" 2>/dev/null || fail "server exited during startup"
  sleep 0.1
done
curl -fs "$base/healthz" > /dev/null || fail "server never became healthy"
echo "ok: /healthz"

# A browser User-Agent is required: curl's default UA is classified as a bot,
# so the $pageview half would be accepted (202) but never stored. The bot
# filter never touches the app or custom halves.
ua='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36'
# shellcheck disable=SC2016  # the $-prefixed names are JSON keys, not shell
code=$(curl -s -o "$dir/events.out" -w '%{http_code}' -A "$ua" -X POST "$base/api/events" \
  -H 'Origin: http://localhost' -H 'Content-Type: application/json' \
  -H "X-Analytics-Key: $key" \
  -d '{"attributes":{"$platform":"ios","$app_version":"1.0","$install_id":"install-1"},
       "events":[
         {"name":"$pageview","attributes":{"$url":"http://localhost/pricing","$referrer":"https://news.ycombinator.com/"}},
         {"name":"$screen_view","attributes":{"$screen":"/settings"}},
         {"name":"signup","attributes":{"plan":"pro"}}]}')
[ "$code" = 202 ] || fail "/api/events returned $code: $(cat "$dir/events.out")"
grep -q '"accepted":3' "$dir/events.out" || fail "expected 3 accepted, got: $(cat "$dir/events.out")"
echo "ok: /api/events accepted 3 events"

# An unknown key is the only auth outcome: 401, never a silent drop.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/api/events" \
  -H 'Content-Type: application/json' -H 'X-Analytics-Key: ak_wrong' \
  -d '{"events":[{"name":"x"}]}')
[ "$code" = 401 ] || fail "/api/events returned $code for a bad key, want 401"
echo "ok: /api/events rejects an unknown key (401)"

# Origin, when present, must match: the browser-abuse deterrent still holds.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/api/events" \
  -H 'Origin: http://evil.example' -H 'Content-Type: application/json' \
  -H "X-Analytics-Key: $key" -d '{"events":[{"name":"x"}]}')
[ "$code" = 403 ] || fail "/api/events returned $code for a bad Origin, want 403"
echo "ok: /api/events rejects a disallowed Origin (403)"

# A retried batch must dedupe on the client-supplied id.
for _ in 1 2; do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/api/events" \
    -H 'Content-Type: application/json' -H "X-Analytics-Key: $key" \
    -d '{"events":[{"id":"018f1e5a-0000-7000-8000-000000000001","name":"replayed"}]}')
  [ "$code" = 202 ] || fail "replay returned $code"
done
echo "ok: /api/events accepts a replayed batch"

# -o /dev/null rather than piping to head: closing the pipe early makes curl
# fail with EPIPE once the snippet grows past one buffer.
curl -fs "$base/js/script.js" -o "$dir/script.js" || fail "/js/script.js not served"
grep -q 'data-key' "$dir/script.js" || fail "/js/script.js is missing data-key wiring"
echo "ok: /js/script.js served"

# Wait past flush_interval, then stop the server cleanly so the buffer drains.
sleep 1
kill "$pid" && wait "$pid" 2>/dev/null || true
pid=""

counts="$(go run ./scripts/smokecheck "$dir/smoke.db")" || fail "could not read database"
echo "rows: $counts"
# product=2: the "signup" event plus one surviving copy of the replayed one.
[ "$counts" = "web=1 app=1 product=2" ] || fail "expected web=1 app=1 product=2, got: $counts"

echo "SMOKE OK"
