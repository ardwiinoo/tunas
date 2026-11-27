#!/bin/sh
# Daily backup of the Tunas database (pg_dump custom format), keeps the newest 7.
# Run on the VPS from cron, e.g.:  15 3 * * * /srv/tunas/backup-db.sh >> /srv/tunas/backup.log 2>&1
#
# Settings (env, with defaults):
#   PG_CONTAINER=postgres  PG_USER=tunas_user  PG_DB=tunas
#   BACKUP_DIR=/srv/backups/tunas  KEEP=7
#   PGPASSWORD (optional; only if local socket auth needs a password)
#   AGE_RECIPIENT  public key (age1...) to encrypt the backup with `age`. The
#                  matching private key is kept by the owner OFF the VPS. The
#                  dump is encrypted as it is written: no plain copy touches
#                  the disk. Without it the backup is plain and the script
#                  says so (a warning, not an error).
set -eu

PG_CONTAINER=${PG_CONTAINER:-postgres}
PG_USER=${PG_USER:-tunas_user}
PG_DB=${PG_DB:-tunas}
BACKUP_DIR=${BACKUP_DIR:-/srv/backups/tunas}
KEEP=${KEEP:-7}
AGE_RECIPIENT=${AGE_RECIPIENT:-}

umask 077
mkdir -p "$BACKUP_DIR"
stamp=$(date +%Y%m%d-%H%M%S)

dump() {
  if [ -n "${PGPASSWORD:-}" ]; then
    docker exec -e PGPASSWORD "$PG_CONTAINER" pg_dump -U "$PG_USER" -d "$PG_DB" -Fc
  else
    docker exec "$PG_CONTAINER" pg_dump -U "$PG_USER" -d "$PG_DB" -Fc
  fi
}

if [ -n "$AGE_RECIPIENT" ]; then
  case "$AGE_RECIPIENT" in
    age1*|ssh-ed25519\ *|ssh-rsa\ *) ;;
    *) echo "AGE_RECIPIENT must be a public key (age1...), never a private key (AGE-SECRET-KEY-...)" >&2; exit 2 ;;
  esac
  command -v age >/dev/null 2>&1 || { echo "AGE_RECIPIENT is set but age is not installed (apt install age)" >&2; exit 1; }
  out="$BACKUP_DIR/$PG_DB-$stamp.dump.age"
  st=$(mktemp)
  trap 'rm -f "$st" "$out.tmp"' EXIT
  # pg_dump's own exit status is kept in $st: a pipeline only reports age's
  { dump; echo $? > "$st"; } | age -r "$AGE_RECIPIENT" -o "$out.tmp" || { echo "backup failed: age could not encrypt" >&2; exit 1; }
  [ "$(cat "$st")" = "0" ] || { echo "backup failed: pg_dump exited with $(cat "$st")" >&2; exit 1; }
  mv "$out.tmp" "$out"
else
  echo "WARNING: AGE_RECIPIENT is not set: this backup is NOT encrypted (see docs/DEPLOY.md, Fase 9d)" >&2
  out="$BACKUP_DIR/$PG_DB-$stamp.dump"
  trap 'rm -f "$out.tmp"' EXIT
  dump > "$out.tmp"
  # an empty dump means pg_dump failed
  [ -s "$out.tmp" ] || { echo "backup failed: empty dump" >&2; exit 1; }
  mv "$out.tmp" "$out"
fi
[ -s "$out" ] || { echo "backup failed: empty file" >&2; rm -f "$out"; exit 1; }

# keep the newest $KEEP backups (plain and encrypted ones count together)
ls -1t "$BACKUP_DIR"/"$PG_DB"-*.dump "$BACKUP_DIR"/"$PG_DB"-*.dump.age 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do
  rm -f "$old"
done

echo "$(date -Iseconds) backup ok: $out ($(wc -c < "$out") bytes)"
