#!/bin/sh
set -eu
cd /work/api/bruno
bru run Workflow --env Local --env-var "baseUrl=${BASE_URL:-http://server:8080}" --env-var "mailpitUrl=http://mailpit:8025" --sandbox=developer --disable-cookies --bail
# Fixture metadata lookup for the delete workflow.
image_id=$(psql -v ON_ERROR_STOP=1 -Atc "SELECT i.id FROM users_images i JOIN users u ON u.id=i.user_id WHERE u.email='bruno-owner@example.test' AND i.deleted_at IS NULL ORDER BY i.id LIMIT 1")
test -n "$image_id" || { echo 'No processed image ID found'; exit 1; }
filename=$(psql -v ON_ERROR_STOP=1 -Atc "SELECT image FROM users_images WHERE id=$image_id AND deleted_at IS NULL")
bru run Delete --env Local --env-var "baseUrl=${BASE_URL:-http://server:8080}" --env-var "mailpitUrl=http://mailpit:8025" --env-var "imageId=$image_id" --env-var "filename=$filename" --sandbox=developer --disable-cookies --bail
