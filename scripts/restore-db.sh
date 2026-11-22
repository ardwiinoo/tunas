#!/bin/sh
# Restore a Tunas dump into a NEW database (never overwrites an existing one).
# Usage: scripts/restore-db.sh <dump file> <target db name>
#   PG_CONTAINER=postgres  PG_USER=tunas_user (must be allowed to CREATE DATABASE)
#
# To switch production to a restored copy, see docs/DEPLOY.md (manual, deliberate step).
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <dump file> <target db>" >&2; exit 2; }
dump=$1
target=$2
PG_CONTAINER=${PG_CONTAINER:-postgres}
PG_USER=${PG_USER:-tunas_user}

case "$target" in
  *[!a-z0-9_]*|"") echo "target db name must match [a-z0-9_]+" >&2; exit 2 ;;
esac
[ -s "$dump" ] || { echo "dump file missing or empty: $dump" >&2; exit 1; }

exists=$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
  "select 1 from pg_database where datname='$target'")
if [ "$exists" = "1" ]; then
  echo "database $target already exists; refusing to overwrite" >&2
  exit 1
fi

docker exec "$PG_CONTAINER" createdb -U "$PG_USER" "$target"
docker exec -i "$PG_CONTAINER" pg_restore -U "$PG_USER" -d "$target" --no-owner --exit-on-error < "$dump"

echo "restored into $target. Row counts:"
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$target" -c \
  "select 'tasks' t, count(*) from tasks union all select 'reminders', count(*) from reminders
   union all select 'devices', count(*) from devices union all select 'activity_buckets', count(*) from activity_buckets
   union all select 'events', count(*) from events"
