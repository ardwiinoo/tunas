#!/bin/sh
# Restore a Tunas dump into a NEW database (never overwrites an existing one).
# Usage: scripts/restore-db.sh <dump file> <target db name> [age identity file]
#   PG_CONTAINER=postgres  PG_USER=tunas_user (must be allowed to CREATE DATABASE)
#
# A dump ending in .age is decrypted on the fly with the private key (age
# identity file) given as the third argument or in AGE_IDENTITY; nothing
# decrypted is written to disk. The private key lives with the owner, off the VPS.
#
# To switch production to a restored copy, see docs/DEPLOY.md (manual, deliberate step).
set -eu

[ $# -ge 2 ] && [ $# -le 3 ] || { echo "usage: $0 <dump file> <target db> [age identity file]" >&2; exit 2; }
dump=$1
target=$2
identity=${3:-${AGE_IDENTITY:-}}
PG_CONTAINER=${PG_CONTAINER:-postgres}
PG_USER=${PG_USER:-tunas_user}

case "$target" in
  *[!a-z0-9_]*|"") echo "target db name must match [a-z0-9_]+" >&2; exit 2 ;;
esac
[ -s "$dump" ] || { echo "dump file missing or empty: $dump" >&2; exit 1; }

encrypted=0
case "$dump" in *.age) encrypted=1 ;; esac
if [ "$encrypted" = 1 ]; then
  command -v age >/dev/null 2>&1 || { echo "age is not installed (apt install age)" >&2; exit 1; }
  [ -n "$identity" ] || { echo "$dump is encrypted: give the private key file as the third argument (or AGE_IDENTITY)" >&2; exit 2; }
  [ -s "$identity" ] || { echo "identity file missing or empty: $identity" >&2; exit 1; }
fi

exists=$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
  "select 1 from pg_database where datname='$target'")
if [ "$exists" = "1" ]; then
  echo "database $target already exists; refusing to overwrite" >&2
  exit 1
fi

docker exec "$PG_CONTAINER" createdb -U "$PG_USER" "$target"
if [ "$encrypted" = 1 ]; then
  # age's own exit status is kept in $st: a pipeline only reports pg_restore's
  st=$(mktemp)
  trap 'rm -f "$st"' EXIT
  rc=0
  { age -d -i "$identity" "$dump"; echo $? > "$st"; } | docker exec -i "$PG_CONTAINER" pg_restore -U "$PG_USER" -d "$target" --no-owner --exit-on-error || rc=$?
  if [ "$(cat "$st")" != "0" ]; then
    echo "decryption failed (wrong private key?). The new database $target is incomplete: drop it before retrying." >&2
    exit 1
  fi
  [ "$rc" = 0 ] || exit "$rc"
else
  docker exec -i "$PG_CONTAINER" pg_restore -U "$PG_USER" -d "$target" --no-owner --exit-on-error < "$dump"
fi

echo "restored into $target. Row counts:"
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$target" -c \
  "select 'tasks' t, count(*) from tasks union all select 'reminders', count(*) from reminders
   union all select 'devices', count(*) from devices union all select 'activity_buckets', count(*) from activity_buckets
   union all select 'events', count(*) from events"
