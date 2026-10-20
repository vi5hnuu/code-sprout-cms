#!/usr/bin/env bash
# First-run preparation for the Code Sprout stack. Safe to run again: nothing that exists is
# overwritten (except the storage config, which is regenerated from .env).
#   - checks .env is filled in
#   - creates the data folders under DATA_DIR
#   - generates the auth service's RSA signing key (once; replacing it signs everyone out)
#   - writes the object storage config with the credentials from .env
#   - prints the lines to add to the host Caddyfile
set -euo pipefail
cd "$(dirname "$0")"

[[ -f .env ]] || { echo "No .env: cp .env.example .env and fill it in first." >&2; exit 1; }
set -a; . ./.env; set +a

if grep -nE '^[^#]*CHANGE_ME' .env >/dev/null; then
    echo "These settings still say CHANGE_ME:" >&2
    grep -nE '^[^#]*CHANGE_ME' .env >&2
    exit 1
fi
for repo in "$WEB_REPO" "$AUTH_REPO"; do
    [[ -d "$repo" ]] || { echo "Repository not found: $repo" >&2; exit 1; }
done

DATA_DIR=${DATA_DIR:-/srv/codesprout}
sudo_if_needed() { if [[ -w "$(dirname "$DATA_DIR")" || -w "$DATA_DIR" ]]; then "$@"; else sudo "$@"; fi; }
sudo_if_needed mkdir -p "$DATA_DIR"/{postgres,redis,seaweedfs,meili,mysql,auth/keys,config,backups}
sudo_if_needed chown "$(id -u):$(id -g)" "$DATA_DIR" "$DATA_DIR"/{auth,auth/keys,config,backups}
# Secrets: other host users cannot reach them (700 parents); the containers, which run as
# their own users, read them through the bind mounts.
chmod 700 "$DATA_DIR/auth" "$DATA_DIR/config" "$DATA_DIR/backups"

keys="$DATA_DIR/auth/keys"
if [[ ! -f "$keys/private.pem" ]]; then
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$keys/private.pem"
    openssl rsa -in "$keys/private.pem" -pubout -out "$keys/public.pem"
    echo "Generated the auth signing key in $keys."
fi
# The auth container runs as its own unprivileged user and only reads the keys.
chmod 755 "$keys"; chmod 644 "$keys/public.pem" "$keys/private.pem"

s3="$DATA_DIR/config/seaweedfs-s3.json"
sed -e "s|__STORAGE_ACCESS_KEY__|$STORAGE_ACCESS_KEY|" -e "s|__STORAGE_SECRET_KEY__|$STORAGE_SECRET_KEY|" \
    seaweedfs-s3.json.example > "$s3"
chmod 600 "$s3" # read by SeaweedFS, which runs as root

here=$(pwd)
cat <<CADDY

Data folders ready in $DATA_DIR. Add to /etc/caddy/Caddyfile, then: sudo systemctl reload caddy

$SITE_DOMAIN {
    import $here/caddy/codesprout-web.caddy
}
$MEDIA_DOMAIN {
    import $here/caddy/codesprout-media.caddy
}
$AUTH_DOMAIN {
    import $here/caddy/codesprout-auth.caddy
}

Next: ./deploy.sh
CADDY
