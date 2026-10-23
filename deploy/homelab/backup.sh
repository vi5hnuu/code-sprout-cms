#!/usr/bin/env bash
# Backs up what cannot be rebuilt, into /var/lib/code-sprout-cms/backups/<timestamp>/:
#   postgres.dump  — the Code Sprout CMS database (pg_dump custom format)
# The search index is rebuilt from Postgres via Studio → Site → "Rebuild search index".
# The auth database is backed up by the ls-judge stack (shared service). Object storage is
# AWS S3; use AWS lifecycle rules or Backup for durability.
#
# Keeps BACKUP_KEEP_DAYS days of local backups (default 14). Example schedule:
#   30 3 * * *  /home/kvi5hnu/Desktop/code-sprout-cms/deploy/homelab/backup.sh
#
# Restore (stack running, into an empty database):
#   docker compose exec -T postgres pg_restore -U sprout -d sprout --clean --if-exists < postgres.dump
set -euo pipefail
cd "$(dirname "$0")"
[[ -f .env ]] || { echo "No ./.env (expected next to this script)." >&2; exit 1; }
set -a; . ./.env; set +a
DATA_ROOT=/var/lib/code-sprout-cms
KEEP_DAYS=${BACKUP_KEEP_DAYS:-14}
umask 077 # dumps hold every account and secret: owner-only
out="$DATA_ROOT/backups/$(date +%Y-%m-%d_%H%M%S)"
sudo mkdir -p "$out"
sudo chown "$(id -u):$(id -g)" "$out"

docker compose exec -T postgres pg_dump -U sprout -d sprout -Fc > "$out/postgres.dump"

sudo find "$DATA_ROOT/backups" -mindepth 1 -maxdepth 1 -type d -mtime +"$KEEP_DAYS" -exec rm -rf {} +
echo "Backup written to $out ($(du -sh "$out" | cut -f1))"
