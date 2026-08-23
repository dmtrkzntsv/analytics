#!/usr/bin/env bash
# End-to-end smoke test: boot the real binary on a scratch database, send a
# pageview and a product event through the HTTP API, and verify both rows land.
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

cat > "$dir/config.json" <<EOF
{
  "listen": "127.0.0.1:$port",
  "database": "sqlite://$dir/smoke.db",
  "geo": "none://",
  "log": { "level": "debug", "format": "text" },
  "buffer": { "flush_max_events": 100, "flush_interval": "200ms", "capacity": 1000 },
  "projects": [
    { "alias": "dev", "name": "Smoke", "allowed_origins": ["http://localhost"] }
  ]
}
EOF

./analytics serve -config "$dir/config.json" > "$dir/log" 2>&1 &
pid=$!

for _ in $(seq 1 50); do
  curl -fs "$base/healthz" > /dev/null 2>&1 && break
  kill -0 "$pid" 2>/dev/null || fail "server exited during startup"
  sleep 0.1
done
curl -fs "$base/healthz" > /dev/null || fail "server never became healthy"
echo "ok: /healthz"

# A browser User-Agent is required: curl's default UA is classified as a
# bot and the hit would be accepted (202) but never stored.
ua='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36'
code=$(curl -s -o "$dir/hit.out" -w '%{http_code}' -A "$ua" -X POST "$base/api/hit" \
  -H 'Origin: http://localhost' -H 'Content-Type: application/json' \
  -d '{"project":"dev","url":"http://localhost/pricing","referrer":"https://news.ycombinator.com/"}')
[ "$code" = 202 ] || fail "/api/hit returned $code: $(cat "$dir/hit.out")"
echo "ok: /api/hit accepted"

# A hit without a matching Origin must be rejected.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/api/hit" \
  -H 'Origin: http://evil.example' -H 'Content-Type: application/json' \
  -d '{"project":"dev","url":"http://localhost/"}')
[ "$code" != 202 ] || fail "/api/hit accepted a disallowed Origin"
echo "ok: /api/hit rejects bad Origin ($code)"

code=$(curl -s -o "$dir/event.out" -w '%{http_code}' -X POST "$base/api/event" \
  -H 'Content-Type: application/json' \
  -d '{"project":"dev","name":"signup","user_id":"user-1","attributes":{"plan":"pro"}}')
[ "$code" = 202 ] || fail "/api/event returned $code: $(cat "$dir/event.out")"
echo "ok: /api/event accepted"

curl -fs "$base/js/script.js" | head -c1 > /dev/null || fail "/js/script.js not served"
echo "ok: /js/script.js served"

# Wait past flush_interval, then stop the server cleanly so the buffer drains.
sleep 1
kill "$pid" && wait "$pid" 2>/dev/null || true
pid=""

counts="$(go run ./scripts/smokecheck "$dir/smoke.db")" || fail "could not read database"
echo "rows: $counts"
[ "$counts" = "web=1 product=1" ] || fail "expected web=1 product=1, got: $counts"

echo "SMOKE OK"
