#!/bin/sh
# Build-and-serve loop for Evidence: rebuild whenever the replica marker
# changes (or every 15 min in aio mode where there is no marker).
set -e
cd /app
[ -d node_modules ] || npm ci
last=""
build() {
  npm run sources && npm run build && echo "evidence: rebuilt $(date -u)"
}
build || true
(npx --yes http-server ./build -p 3000 -s &)
while true; do
  cur=$(cat /data/.last_sync 2>/dev/null || date -u +%Y-%m-%dT%H:%M)
  if [ "$cur" != "$last" ]; then
    last="$cur"
    build || echo "evidence: build failed, serving previous"
  fi
  sleep 60
done
