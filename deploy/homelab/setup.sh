#!/usr/bin/env bash
# First-run preparation for the Code Sprout CMS stack on the homelab host.
# Run as root: `sudo -E ./setup.sh`.
#
# Idempotent — safe to re-run. On each run:
#   • creates data folders under /var/lib/code-sprout-cms/
#   • creates log folders under /var/log/code-sprout-cms/
#   • materialises /etc/code-sprout-cms/prod.env from ./prod.env.template:
#       - __AUTO_SECRET__ markers are replaced with `openssl rand -hex 32` on first run
#       - ${REFERENCED_VAR} references are resolved against values already set above
#       - secret values already present in the live prod.env are kept (never rotated)
#   • prints the remaining manual steps (shared-auth audience, Caddy snippets, S3 buckets)
#
# Preconditions: a ./env file next to this script (copy from .env.example).
set -euo pipefail
cd "$(dirname "$0")"

[[ $EUID -eq 0 ]] || { echo "setup.sh must run as root (sudo -E $0)" >&2; exit 1; }

# ── env ──────────────────────────────────────────────────────────────────────
[[ -f .env ]] || { echo "No ./.env: cp .env.example .env and fill it in first." >&2; exit 1; }
set -a; . ./.env; set +a

: "${WEB_REPO:?set WEB_REPO in ./env (the laxmi-solutions-nextjs checkout)}"
: "${SITE_DOMAIN:?set SITE_DOMAIN in ./env}"
: "${API_DOMAIN:?set API_DOMAIN in ./env}"
[[ -d "$WEB_REPO" ]] || { echo "WEB_REPO does not exist: $WEB_REPO" >&2; exit 1; }

# ── data + log folders ──────────────────────────────────────────────────────
DATA_ROOT=/var/lib/code-sprout-cms
LOG_ROOT=/var/log/code-sprout-cms
install -d -m 0755 \
    "$DATA_ROOT"/postgres \
    "$DATA_ROOT"/redis \
    "$DATA_ROOT"/meili \
    "$DATA_ROOT"/backups \
    "$LOG_ROOT"/api \
    "$LOG_ROOT"/worker \
    "$LOG_ROOT"/web

# ── prod.env ────────────────────────────────────────────────────────────────
TARGET=/etc/code-sprout-cms/prod.env
install -d -m 0750 "$(dirname "$TARGET")"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

# Carry existing values over from the live file, so re-runs do not rotate secrets.
declare -A existing
if [[ -r "$TARGET" ]]; then
    while IFS= read -r line; do
        [[ "$line" =~ ^([A-Z_][A-Z0-9_]*)=(.*)$ ]] || continue
        existing[${BASH_REMATCH[1]}]=${BASH_REMATCH[2]}
    done < "$TARGET"
fi

# Resolve the template line by line. values[key] holds the fully-resolved value so later
# lines can reference it with ${KEY}; _substitute() does the ${KEY} expansion.
declare -A values
_substitute() {
    local raw=$1
    local out=$raw
    local key v
    while [[ "$out" =~ \$\{([A-Z_][A-Z0-9_]*)\} ]]; do
        key=${BASH_REMATCH[1]}
        v=${values[$key]-}
        [[ -n "$v" ]] || { echo "setup.sh: template references undefined \${$key}" >&2; exit 1; }
        out=${out//\$\{$key\}/$v}
    done
    printf '%s' "$out"
}

while IFS= read -r line; do
    if [[ "$line" =~ ^([A-Z_][A-Z0-9_]*)=(.*)$ ]]; then
        key=${BASH_REMATCH[1]}; raw=${BASH_REMATCH[2]}
        if [[ -n "${existing[$key]-}" ]]; then
            val=${existing[$key]}
        elif [[ "$raw" == '__AUTO_SECRET__' ]]; then
            val=$(openssl rand -hex 32)
        else
            val=$(_substitute "$raw")
        fi
        values[$key]=$val
        printf '%s=%s\n' "$key" "$val" >> "$tmp"
    else
        printf '%s\n' "$line" >> "$tmp"
    fi
done < prod.env.template

install -m 0600 -o root -g root "$tmp" "$TARGET"
echo "Wrote $TARGET (0600 root:root)."

# ── reminders ────────────────────────────────────────────────────────────────
cat <<MANUAL

Next steps the operator still owns:

1) Shared auth. Add this stack's audience, then restart it:

     # /etc/ls-judge/auth.env  (append to AUTH_JWT_ALLOWED_AUDIENCES, comma-separated)
     AUTH_JWT_ALLOWED_AUDIENCES=ls-judge-api,code-sprout-cms

     sudo systemctl restart ls-judge-auth

2) AWS S3 buckets. The CMS needs a public bucket (images, videos — CDN-fronted) and a
   private bucket (test sets, checkers, archived revisions). Create them in the AWS console
   (ap-south-1), then fill in these three in $TARGET:
     STORAGE_PUBLIC_BUCKET=<name>
     STORAGE_PUBLIC_BASE_URL=https://<cdn-hostname>
     STORAGE_PRIVATE_BUCKET=<name>
   And update MEDIA_DOMAIN in ./env to match the CDN hostname (used at web-image build time).

3) Caddy. Copy the two snippets to /etc/caddy and add the two site blocks to the Caddyfile:

     cp caddy/code-sprout-cms-web.caddy /etc/caddy/
     cp caddy/code-sprout-cms-api.caddy /etc/caddy/

     # Add to /etc/caddy/Caddyfile:
     $SITE_DOMAIN {
         import /etc/caddy/code-sprout-cms-web.caddy
     }
     $API_DOMAIN {
         import /etc/caddy/code-sprout-cms-api.caddy
     }

     # The legacy snippet for the old code-sprout API (localhost:8084) is now stale; its
     # import can be dropped from the Caddyfile and the file removed:
     #     rm /etc/caddy/code-sprout.caddy

     caddy validate --config /etc/caddy/Caddyfile
     systemctl reload caddy

4) Build and start the stack:  sudo -E ./deploy.sh
MANUAL
