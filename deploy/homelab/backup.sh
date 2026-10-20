#!/usr/bin/env bash
# Backs up everything that cannot be rebuilt, into $DATA_DIR/backups/<timestamp>/:
#   postgres.dump  - Code Sprout database (pg_dump custom format)
#   auth.sql.gz    - auth database (accounts, sessions)
#   storage.tar.gz - uploaded files (SeaweedFS data)
#   auth-keys/     - the auth signing key
# The search index is not backed up: Studio → Site → "Rebuild search index" recreates it.
# Keeps BACKUP_KEEP_DAYS days (default 14). Schedule it, e.g. crontab: 30 3 * * * /path/backup.sh
#
# Restore (stack running, into empty databases):
#   docker compose exec -T postgres pg_restore -U sprout -d sprout --clean --if-exists < postgres.dump
#   gunzip -c auth.sql.gz | docker compose exec -T auth-db sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -u root auth_db'
#   docker compose stop seaweedfs && tar -xzf storage.tar.gz -C "$DATA_DIR" && docker compose start seaweedfs
#   cp auth-keys/* "$DATA_DIR/auth/keys/"
set -euo pipefail
cd "$(dirname "$0")"
set -a; . ./.env; set +a
DATA_DIR=${DATA_DIR:-/srv/codesprout}
umask 077 # dumps hold every account and secret: owner-only
out="$DATA_DIR/backups/$(date +%Y-%m-%d_%H%M%S)"
mkdir -p "$out"

docker compose exec -T postgres pg_dump -U sprout -d sprout -Fc > "$out/postgres.dump"
docker compose exec -T auth-db sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -u root --single-transaction --routines auth_db' | gzip > "$out/auth.sql.gz"
# Files are written once and never changed in place, so a live copy is consistent.
tar -czf "$out/storage.tar.gz" -C "$DATA_DIR" seaweedfs
cp -r "$DATA_DIR/auth/keys" "$out/auth-keys"

find "$DATA_DIR/backups" -mindepth 1 -maxdepth 1 -type d -mtime +"${BACKUP_KEEP_DAYS:-14}" -exec rm -rf {} +
echo "Backup written to $out ($(du -sh "$out" | cut -f1))"
