#!/usr/bin/env bash
# Coverage gate per spec §14: total >= 80%, core packages >= 85%.
set -euo pipefail

packages=$(go list ./... | grep -v '/cmd/' || true)
if [ -z "$packages" ]; then
  echo "no packages yet"
  exit 0
fi

go test -race -coverprofile=coverage.out $packages

total=$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
echo "total coverage: ${total}%"
awk -v t="$total" 'BEGIN { exit (t+0 >= 80.0) ? 0 : 1 }' \
  || { echo "FAIL: total coverage ${total}% < 80%"; exit 1; }

core="internal/store internal/enrich internal/pipeline internal/identity internal/config"
fail=0
for pkg in $core; do
  pct=$(go tool cover -func=coverage.out \
    | awk -v p="$pkg/" 'index($1, p) {gsub(/%/,"",$3); s+=$3; n++} END {if (n) printf "%.1f", s/n; else print "-"}')
  [ "$pct" = "-" ] && continue  # package not written yet
  echo "  $pkg: ${pct}%"
  awk -v t="$pct" 'BEGIN { exit (t+0 >= 85.0) ? 0 : 1 }' \
    || { echo "FAIL: $pkg coverage ${pct}% < 85%"; fail=1; }
done
exit $fail
