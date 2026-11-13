#!/usr/bin/env bash
set -euo pipefail

deploy_dir=$1
release=$2
export APP_IMAGE=$3
ln -sfn "$deploy_dir/.env" "$release/.env"
cd "$release"
compose=(docker compose --env-file "$deploy_dir/.env" -f "$release/compose.yaml")

"${compose[@]}" config -q
"${compose[@]}" pull
"${compose[@]}" up -d --wait postgres rabbitmq redis
"${compose[@]}" run --rm --no-deps migrate
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 server worker outbox
printf '%s\n' "$APP_IMAGE" > "$deploy_dir/current-image"
printf '%s\n' "$release" > "$deploy_dir/current-release"
