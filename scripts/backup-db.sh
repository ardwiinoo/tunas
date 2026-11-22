#!/bin/sh
# Daily backup of the Tunas database (pg_dump custom format), keeps the newest 7.
# Run on the VPS from cron, e.g.:  15 3 * * * /srv/tunas/backup-db.sh >> /srv/tunas/backup.log 2>&1
#
# Settings (env, with defaults):
#   PG_CONTAINER=postgres  PG_USER=tunas_user  PG_DB=tunas
#   BACKUP_DIR=/srv/backups/tunas  KEEP=7
#   PGPASSWORD (optional; only if local socket auth needs a password)
set -eu

PG_CONTAINER=${PG_CONTAINER:-postgres}
PG_USER=${PG_USER:-tunas_user}
PG_DB=${PG_DB:-tunas}
BACKUP_DIR=${BACKUP_DIR:-/srv/backups/tunas}
KEEP=${KEEP:-7}

umask 077
mkdir -p "$BACKUP_DIR"
out="$BACKUP_DIR/$PG_DB-$(date +%Y%m%d-%H%M%S).dump"

if [ -n "${PGPASSWORD:-}" ]; then
  docker exec -e PGPASSWORD "$PG_CONTAINER" pg_dump -U "$PG_USER" -d "$PG_DB" -Fc > "$out.tmp"
else
  docker exec "$PG_CONTAINER" pg_dump -U "$PG_USER" -d "$PG_DB" -Fc > "$out.tmp"
fi
# an empty dump means pg_dump failed
[ -s "$out.tmp" ] || { echo "backup failed: empty dump" >&2; rm -f "$out.tmp"; exit 1; }
mv "$out.tmp" "$out"

# keep the newest $KEEP dumps
ls -1t "$BACKUP_DIR"/"$PG_DB"-*.dump 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do
  rm -f "$old"
done

echo "$(date -Iseconds) backup ok: $out ($(wc -c < "$out") bytes)"
