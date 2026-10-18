#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
ROOT=/opt/laravel-erp
CONFIG=/etc/laravel-erp
BACKUP_ENV="$CONFIG/backup.env"
[[ $EUID -eq 0 ]] || { echo "Spusťte přes sudo/root." >&2; exit 1; }
[[ -r "$CONFIG/stack.env" && -r "$CONFIG/secrets.env" && -r "$BACKUP_ENV" ]] || { echo "Chybí stack.env, secrets.env nebo backup.env." >&2; exit 1; }
command -v restic >/dev/null 2>&1 || { echo "Není nainstalován restic." >&2; exit 1; }
set -a; source "$CONFIG/stack.env"; source "$CONFIG/secrets.env"; source "$BACKUP_ENV"; set +a
[[ -n "${RESTIC_REPOSITORY:-}" && -n "${RESTIC_PASSWORD:-}" ]] || { echo "Nastavte RESTIC_REPOSITORY a RESTIC_PASSWORD v $BACKUP_ENV." >&2; exit 1; }
dc(){ docker compose --project-directory "$ROOT" --env-file "$CONFIG/stack.env" --env-file "$CONFIG/secrets.env" "$@"; }
mkdir -p "$ROOT/backups-tmp"
chmod 0700 "$ROOT/backups-tmp"
TMPDIR="$(mktemp -d "$ROOT/backups-tmp/run.XXXXXX")"
trap 'rm -rf -- "$TMPDIR"' EXIT
DUMP="$TMPDIR/mariadb.sql.gz"
echo "Vytvářím konzistentní logický dump DB..."
dc exec -T db sh -ec 'exec mariadb-dump --single-transaction --routines --events --triggers --hex-blob -u root -p"$MARIADB_ROOT_PASSWORD" "$MARIADB_DATABASE"' | gzip -9 > "$DUMP"
gzip -t "$DUMP"
export RESTIC_REPOSITORY RESTIC_PASSWORD
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-}" AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-}"
restic snapshots >/dev/null 2>&1 || restic init
restic backup "$DUMP" "$ROOT/persistent/storage" "$ROOT/persistent/public" --tag laravel-erp --host "$(hostname -f)"
restic forget --tag laravel-erp --keep-daily 7 --keep-weekly 5 --keep-monthly 12 --prune
echo "Záloha odeslána do externího repository. Proveďte pravidelný test obnovy."
