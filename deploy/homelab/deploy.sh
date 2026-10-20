#!/usr/bin/env bash
# Builds and (re)starts the stack in dependency order, then reports health. Run after
# setup.sh, and again after pulling new code. Data is never touched.
#
# The order matters for the web image: its build prerenders the static pages (home, hubs,
# feeds) from the running API, so the backend is up before the web image is built.
set -euo pipefail
cd "$(dirname "$0")"
[[ -f .env ]] || { echo "No .env: run setup.sh first." >&2; exit 1; }

compose() { docker compose "$@"; }

echo "→ Building backend images"
compose build migrate api worker auth

echo "→ Starting data stores, auth, migrations and the API"
compose up -d --wait postgres redis seaweedfs meilisearch auth-db auth
compose up -d migrate storage-init
compose up -d --wait api worker

echo "→ Building the web image (prerendering from the API)"
compose build web
compose up -d --wait web

echo "→ Status"
compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Status}}'
for one_shot in migrate storage-init; do
    code=$(docker inspect -f '{{.State.ExitCode}}' "$(compose ps -a -q "$one_shot")")
    [[ "$code" == "0" ]] || { echo "$one_shot failed (exit $code): docker compose logs $one_shot" >&2; exit 1; }
done
echo "Done. Old images can be pruned with: docker image prune -f"
