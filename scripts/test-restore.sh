#!/usr/bin/env bash
# Exercises deploy/litestream/restore.sh against a stubbed litestream binary:
# a successful restore must install the replica, and every failure — litestream
# exiting nonzero, a corrupt download, a restore that yields a database with
# no twillingate schema in it — must leave the previous replica byte-for-byte
# intact. The empty cases are not hypothetical: a `path:` that does not match
# the writer's, or a bucket written by a different litestream major/minor,
# restores a valid database with nothing in it and exits 0. One case runs in
# loop mode, where the `restore_once || …` caller disables set -e inside the
# function; that path once installed an empty database over a good replica.
# Requires sqlite3; litestream itself is stubbed.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v sqlite3 > /dev/null || { echo "sqlite3 is required"; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
export PATH="$work/bin:$PATH"

# The stub takes its output path from -o like the real CLI and reads its
# behaviour from LITESTREAM_STUB_MODE.
cat > "$work/bin/litestream" << 'EOF'
#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  [ "$1" = "-o" ] && { out="$2"; shift; }
  shift
done
case "${LITESTREAM_STUB_MODE:-ok}" in
  ok)      sqlite3 "$out" 'CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);
                           INSERT INTO schema_migrations VALUES (1);
                           CREATE TABLE t(x); INSERT INTO t VALUES (1);' ;;
  fail)    echo "no matching backups found" >&2; exit 1 ;;
  corrupt) printf 'not a sqlite database' > "$out" ;;
  # What an identity or version mismatch actually produces: a database, not
  # an error.
  empty)   sqlite3 "$out" 'CREATE TABLE unrelated(x);' ;;
  bare)    sqlite3 "$out" 'CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);' ;;
esac
EOF
chmod +x "$work/bin/litestream"

export REPLICA_PATH="$work/replica.db"
export LITESTREAM_CONFIG="$work/litestream.yml"
: > "$LITESTREAM_CONFIG"

fail() { echo "FAIL: $*" >&2; exit 1; }

# A successful restore installs a verified replica.
LITESTREAM_STUB_MODE=ok sh deploy/litestream/restore.sh \
  || fail "successful restore exited nonzero"
[ -s "$REPLICA_PATH" ] || fail "successful restore did not install a replica"
before="$(cksum "$REPLICA_PATH")"

# litestream failing must exit nonzero and must not touch the replica.
if LITESTREAM_STUB_MODE=fail sh deploy/litestream/restore.sh 2> /dev/null; then
  fail "failed restore exited 0"
fi
[ "$(cksum "$REPLICA_PATH")" = "$before" ] || fail "failed restore replaced the replica"

# A corrupt download must not be installed either.
if LITESTREAM_STUB_MODE=corrupt sh deploy/litestream/restore.sh 2> /dev/null; then
  fail "corrupt restore exited 0"
fi
[ "$(cksum "$REPLICA_PATH")" = "$before" ] || fail "corrupt restore replaced the replica"

# A restore that succeeds and yields a database with no twillingate schema is
# the one that looks like success: litestream exits 0 and the file passes
# quick_check, so only the schema tells it from a real replica.
if LITESTREAM_STUB_MODE=empty sh deploy/litestream/restore.sh 2> /dev/null; then
  fail "restore of a database with no schema exited 0"
fi
[ "$(cksum "$REPLICA_PATH")" = "$before" ] || fail "empty restore replaced the replica"

# The schema present but unpopulated — a database the driver created rather
# than one a migration run built.
if LITESTREAM_STUB_MODE=bare sh deploy/litestream/restore.sh 2> /dev/null; then
  fail "restore of an unmigrated database exited 0"
fi
[ "$(cksum "$REPLICA_PATH")" = "$before" ] || fail "unmigrated restore replaced the replica"

# The same failure in loop mode, where `restore_once || …` turns set -e off
# inside the function. timeout ends the loop; only the replica's fate matters.
LITESTREAM_STUB_MODE=fail LOOP_INTERVAL=1 timeout 3 \
  sh deploy/litestream/restore.sh 2> /dev/null || true
[ "$(cksum "$REPLICA_PATH")" = "$before" ] || fail "loop-mode failure replaced the replica"

# And in loop mode for the empty restore, which is the path that would
# overwrite a good replica without ever exiting nonzero.
LITESTREAM_STUB_MODE=empty LOOP_INTERVAL=1 timeout 3 \
  sh deploy/litestream/restore.sh 2> /dev/null || true
[ "$(cksum "$REPLICA_PATH")" = "$before" ] || fail "loop-mode empty restore replaced the replica"

echo "PASS: restore.sh keeps the previous replica on every failure path"
