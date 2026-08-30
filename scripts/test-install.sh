#!/usr/bin/env bash
# Runs install.sh inside a throwaway Debian container (with systemctl
# stubbed — no systemd in a container) and asserts every artifact lands where
# the runbook says. Requires docker and a linux binary at the repo root.
# The container-side assertions live in test-install-inner.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

[ -x twillingate ] || { echo "run 'make build' first"; exit 1; }
command -v docker > /dev/null || { echo "docker is required"; exit 1; }

docker run --rm -v "$PWD:/src:ro" debian:bookworm-slim \
  bash /src/scripts/test-install-inner.sh
