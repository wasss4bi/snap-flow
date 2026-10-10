#!/usr/bin/env bash
set -euo pipefail

deploy_dir=$1
release=$2
export APP_IMAGE=$3
ln -sfn "$deploy_dir/.env" "$release/.env"
cd "$release"
compose=(docker compose --env-file "$deploy_dir/.env" -f "$release/compose.yaml" -f "$release/compose.observability.yaml" -f "$release/compose.observability.production.yaml")
previous_image=''
previous_release=''
if [[ -f "$deploy_dir/current-image" && -f "$deploy_dir/current-release" ]]; then
    previous_image=$(<"$deploy_dir/current-image")
    previous_release=$(<"$deploy_dir/current-release")
fi
if [[ -n "$previous_release" && -f "$previous_release/compose.yaml" ]] &&
   ! grep -q 'STORAGE_ENCRYPTION_KEY_VERSION' "$previous_release/compose.yaml" &&
   ! grep -Eq "^[[:space:]]*STORAGE_WRITE_LEGACY=(true|\"true\"|'true')[[:space:]]*$" "$deploy_dir/.env"; then
    echo 'Previous release cannot read versioned objects; set STORAGE_WRITE_LEGACY=true for the first upgrade.' >&2
    exit 1
fi
rollback() {
    local status=$?
    trap - ERR
    if [[ -z "$previous_image" || -z "$previous_release" || ! -f "$previous_release/compose.yaml" ]]; then
        echo 'Deployment failed; no previous release is available for rollback.' >&2
        exit "$status"
    fi
    echo "Deployment failed; restoring previous application release." >&2
    export APP_IMAGE="$previous_image"
    local -a old_compose=(docker compose --env-file "$deploy_dir/.env" -f "$previous_release/compose.yaml" -f "$previous_release/compose.observability.yaml" -f "$previous_release/compose.observability.production.yaml")
    if ! "${old_compose[@]}" up -d --no-deps --wait --wait-timeout 180 server worker outbox ||
       ! "${old_compose[@]}" up -d --wait --wait-timeout 180 prometheus grafana node-exporter blackbox-exporter alertmanager alertbridge; then
        echo 'Rollback failed; manual intervention is required.' >&2
    else
        printf '%s\n' "$previous_image" > "$deploy_dir/current-image" || true
        printf '%s\n' "$previous_release" > "$deploy_dir/current-release" || true
    fi
    exit "$status"
}

"${compose[@]}" config -q
"${compose[@]}" pull
"${compose[@]}" up -d --wait postgres rabbitmq redis
"${compose[@]}" run --rm --no-deps -T --interactive=false migrate
trap rollback ERR
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 server worker outbox
"${compose[@]}" up -d --wait --wait-timeout 180 prometheus grafana node-exporter blackbox-exporter alertmanager alertbridge
printf '%s\n' "$APP_IMAGE" > "$deploy_dir/current-image"
printf '%s\n' "$release" > "$deploy_dir/current-release"
trap - ERR
