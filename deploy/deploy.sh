#!/usr/bin/env bash
set -Eeuo pipefail

deploy_root="${1:?deployment root is required}"
release_id="${2:?release id is required}"
healthcheck_url="${3:?healthcheck URL is required}"

release_dir="$deploy_root/releases/$release_id"
shared_env="$deploy_root/shared/.env"
compose_file="$release_dir/compose.production.yml"
images_env="$release_dir/images.env"
current_link="$deploy_root/current"
previous_dir=""

for required_file in "$shared_env" "$compose_file" "$images_env"; do
  if [ ! -f "$required_file" ]; then
    echo "Required deployment file is missing: $required_file" >&2
    exit 1
  fi
done

if [ -L "$current_link" ]; then
  previous_dir="$(readlink -f "$current_link")"
fi

compose_release() {
  local target_dir="$1"
  shift
  docker compose \
    -p statzavod \
    --env-file "$shared_env" \
    --env-file "$target_dir/images.env" \
    -f "$target_dir/compose.production.yml" \
    "$@"
}

rollback() {
  local exit_code=$?
  trap - ERR
  echo "Deployment failed. Collecting logs..." >&2
  compose_release "$release_dir" logs --tail=200 || true

  if [ -n "$previous_dir" ] &&
     [ -f "$previous_dir/compose.production.yml" ] &&
     [ -f "$previous_dir/images.env" ]; then
    echo "Restoring the previous application images..." >&2
    compose_release "$previous_dir" pull api worker web || true
    compose_release "$previous_dir" up -d --remove-orphans --wait --wait-timeout 180 || true
  else
    echo "No previous release is available for automatic rollback." >&2
  fi

  exit "$exit_code"
}
trap rollback ERR

compose_release "$release_dir" config --quiet
compose_release "$release_dir" pull
compose_release "$release_dir" up -d --remove-orphans --wait --wait-timeout 180

curl \
  --fail \
  --silent \
  --show-error \
  --retry 12 \
  --retry-delay 5 \
  --retry-all-errors \
  --max-time 15 \
  "$healthcheck_url" >/dev/null

ln -sfn "$release_dir" "$current_link"
trap - ERR

echo "Release $release_id is healthy and active."
