#!/usr/bin/env bash
# End-to-end test of the published topology: build both images, run the
# single-server compose stack, send a hit, and wait for the dashboards to
# render it. This is the one test that exercises the real Evidence build, so
# it is slow (a first build takes about a minute) and manual — `make check`
# does not run it.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker > /dev/null || { echo "docker is required"; exit 1; }

dir="$(mktemp -d)"
project="twillingate-composetest-$$"
cleanup() {
  docker compose -p "$project" -f "$dir/docker-compose.yml" down -v > /dev/null 2>&1 || true
  rm -rf "$dir"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "building images..."
docker build --target runtime -t twillingate:composetest . > /dev/null
docker build --target evidence -t twillingate-evidence:composetest . > /dev/null

# The compose file names published images; the test substitutes the ones it
# just built so it exercises this working tree rather than the registry.
# shellcheck disable=SC2016  # the ${TWILLINGATE_VERSION} text is matched, not expanded
sed -e 's|ghcr.io/dmtrkzntsv/twillingate:${TWILLINGATE_VERSION:-latest}|twillingate:composetest|' \
    -e 's|ghcr.io/dmtrkzntsv/twillingate-evidence:${TWILLINGATE_VERSION:-latest}|twillingate-evidence:composetest|' \
    -e 's|"8080:8080"|"18080:8080"|' \
    -e 's|"3000:3000"|"13000:3000"|' \
    deploy/compose/docker-compose.yml > "$dir/docker-compose.yml"

docker compose -p "$project" -f "$dir/docker-compose.yml" up -d > /dev/null

echo "waiting for ingestion..."
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "http://127.0.0.1:18080/js/script.js" 2>/dev/null && break
  sleep 1
done

# Projects live in the database now: seed one through the CLI inside the
# running container instead of mounting a projects.json.
echo "creating project via the CLI..."
docker compose -p "$project" -f "$dir/docker-compose.yml" exec -T twillingate \
  /usr/local/bin/twillingate project create -alias dev -name Dev -origin "http://localhost:18080" \
  || fail "project create failed"
key="$(docker compose -p "$project" -f "$dir/docker-compose.yml" exec -T twillingate \
  /usr/local/bin/twillingate key issue -project dev -label web | grep -o 'ak_[0-9a-f]*' | head -1)"
[ -n "$key" ] || fail "key issue failed"

# curl's default User-Agent is classified as a bot and the pageview would be
# accepted but never stored, so the request has to look like a browser.
ua='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36'
# shellcheck disable=SC2016  # the $-prefixed names are JSON keys, not shell
post_events() {
  curl -s -o "$dir/events.out" -w '%{http_code}' -A "$ua" -X POST "http://127.0.0.1:18080/api/events" \
    -H 'Origin: http://localhost:18080' -H 'Content-Type: application/json' \
    -H "X-Analytics-Key: $key" \
    -d '{"attributes":{"$platform":"ios","$app_version":"1.0","$install_id":"install-1"},
         "events":[
           {"name":"$pageview","attributes":{"$url":"http://localhost:18080/pricing"}},
           {"name":"$screen_view","attributes":{"$screen":"/settings"}},
           {"name":"signup","attributes":{"plan":"pro"}}]}'
}
# The key was just written out-of-process (docker compose exec), and the
# server's in-memory registry only notices out-of-process writes via its
# config_version poll (internal/manage/registry.go: pollInterval = 1s), so
# the very first POST can race that poll and 401. Retry rather than sleep
# blindly: it self-documents the propagation window instead of hard-coding
# a guess at it.
code=""
for _ in $(seq 1 10); do
  code="$(post_events)"
  [ "$code" = "202" ] && break
  [ "$code" = "401" ] || break
  sleep 0.5
done
[ "$code" = "202" ] || fail "/api/events returned $code: $(cat "$dir/events.out")"

echo "waiting for the first Evidence build (this takes about a minute)..."
status=""
for _ in $(seq 1 60); do
  status="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:13000/")"
  [ "$status" = "200" ] && break
  sleep 5
done
[ "$status" = "200" ] || fail "dashboards never left $status"

curl -fsS "http://127.0.0.1:13000/" | grep -q "<title>" || fail "index is not a rendered page"

# Every templated route, including the drill-down that only prerenders when
# its query-string read is guarded for the prerender pass.
for page in /web/dev/ /app/dev/ /product/dev/ /users/dev/ /groups/dev/ /retention/dev/ /web/dev/page/; do
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:13000$page")"
  [ "$code" = "200" ] || fail "$page returned $code"
done

# A rendered page is not enough: sources must have produced data. The parquet
# extracts are what the browser queries, and an empty build still serves 200.
docker compose -p "$project" -f "$dir/docker-compose.yml" exec -T dashboards \
  sh -c 'find /opt/evidence/site.* -name "*.parquet" -size +1k | head -1' | grep -q parquet \
  || fail "no non-trivial parquet extracts in the served site"

echo "PASS: ingestion on :18080, dashboards rendered on :13000"
