#!/usr/bin/env bash
# Builds and (re)starts the Code Sprout CMS stack. Run as root: `sudo -E ./deploy.sh`.
#
# Root is needed because /etc/code-sprout-cms/prod.env (the compose env_file) is 0600
# root:root — the same convention every other service on this host uses.
#
# Order matters for the web image: its build prerenders the static pages (home, hubs, feeds)
# from the running API, so the backend is up before the web image is built.
set -euo pipefail
cd "$(dirname "$0")"

[[ $EUID -eq 0 ]] || { echo "deploy.sh must run as root (sudo -E $0)" >&2; exit 1; }
[[ -f .env ]] || { echo "No ./.env: run setup.sh first." >&2; exit 1; }
[[ -r /etc/code-sprout-cms/prod.env ]] || { echo "/etc/code-sprout-cms/prod.env missing: run setup.sh first." >&2; exit 1; }
set -a; . ./.env; set +a

compose() { docker compose "$@"; }

echo "→ Building backend images"
compose build migrate api worker

echo "→ Starting data stores, running migrations, then API + worker"
compose up -d --wait postgres redis meilisearch
compose up -d migrate
compose up -d --wait api worker

echo "→ Building the web image (prerendering from the API)"
compose build web
compose up -d --wait web

echo "→ Status"
compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Status}}'
code=$(docker inspect -f '{{.State.ExitCode}}' "$(compose ps -a -q migrate)")
[[ "$code" == "0" ]] || { echo "migrate failed (exit $code): docker compose logs migrate" >&2; exit 1; }

echo
echo "Done. If Caddy is already set up, the site is live at https://${SITE_DOMAIN}."
echo "Old images can be pruned with: docker image prune -f"
