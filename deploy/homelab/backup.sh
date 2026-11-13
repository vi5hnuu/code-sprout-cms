#!/usr/bin/env bash
# Backs up what cannot be rebuilt, into /var/lib/code-sprout-cms/backups/<timestamp>/:
#   postgres.dump  — the Code Sprout CMS database (pg_dump custom format)
#   base.tar.gz    — a physical base backup, only with PG_ARCHIVE_MODE=on (see ./.env)
# The search index is rebuilt from Postgres via Studio → Site → "Rebuild search index".
# The auth database is backed up by the ls-judge stack (shared service). Object storage is
# AWS S3; use AWS lifecycle rules or Backup for durability.
#
# Keeps BACKUP_KEEP_DAYS days of local backups (default 14), and with archiving on, the same
# days of WAL. Example schedule:
#   30 3 * * *  /home/kvi5hnu/Desktop/code-sprout-cms/deploy/homelab/backup.sh
#
# Restore from the dump (stack running, into an empty database):
#   docker compose exec -T postgres pg_restore -U sprout -d sprout --clean --if-exists < postgres.dump
#
# Point-in-time restore (PG_ARCHIVE_MODE=on; recovers to any moment after the base backup):
#   1. docker compose stop postgres; move /var/lib/code-sprout-cms/postgres aside.
#   2. Unpack base.tar.gz into a fresh /var/lib/code-sprout-cms/postgres (owner uid 70).
#   3. In it: touch recovery.signal, and append to postgresql.auto.conf
#        restore_command = 'cp /var/lib/postgresql/wal_archive/%f %p'
#        recovery_target_time = '2026-11-11 09:30:00+05:30'   # omit to replay everything
#        recovery_target_action = 'promote'
#   4. docker compose start postgres; watch `docker compose logs -f postgres` until promoted.
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

# A base backup is the starting point WAL replay needs. -X none: the WAL it needs is in the
# archive already, so the tarball stays small and consistent with it.
if [[ "${PG_ARCHIVE_MODE:-off}" == "on" ]]; then
    docker compose exec -T postgres pg_basebackup -U sprout -D - -Ft -z -X none > "$out/base.tar.gz"
    # WAL older than the oldest kept base backup can no longer be replayed from anything.
    sudo find "$DATA_ROOT/wal_archive" -type f -mtime +"$KEEP_DAYS" -delete
fi

sudo find "$DATA_ROOT/backups" -mindepth 1 -maxdepth 1 -type d -mtime +"$KEEP_DAYS" -exec rm -rf {} +
echo "Backup written to $out ($(du -sh "$out" | cut -f1))"
