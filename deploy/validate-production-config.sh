#!/usr/bin/env sh
set -eu

if [ "${APP_ENV:-development}" != "production" ]; then
  if [ "$#" -gt 0 ]; then
    exec "$@"
  fi
  exit 0
fi

fail() {
  printf '%s\n' "production media configuration: $*" >&2
  exit 1
}

upload_origin="${MEDIA_UPLOAD_ORIGIN:-}"
storage_endpoint="${MEDIA_S3_ENDPOINT:-}"

case "$upload_origin" in
  https://*/*|https://*\?*|https://*\#*|https://*@*|https://*:*)
    fail "MEDIA_UPLOAD_ORIGIN must be an exact HTTPS origin without credentials, port, path, query, or fragment"
    ;;
  https://?*) ;;
  *) fail "MEDIA_UPLOAD_ORIGIN must be an exact HTTPS origin" ;;
esac

case "$storage_endpoint" in
  https://*/)
    storage_origin=${storage_endpoint%/}
    ;;
  https://*/*|https://*\?*|https://*\#*|https://*@*|https://*:*)
    fail "MEDIA_S3_ENDPOINT must be a clean HTTPS origin"
    ;;
  https://?*)
    storage_origin=$storage_endpoint
    ;;
  *) fail "MEDIA_S3_ENDPOINT must be a clean HTTPS origin" ;;
esac

[ "$upload_origin" = "$storage_origin" ] || fail "MEDIA_UPLOAD_ORIGIN must equal the MEDIA_S3_ENDPOINT origin"

cors_origin="${CORS_ORIGIN:-}"
case "$cors_origin" in
  https://*/*|https://*\?*|https://*\#*|https://*@*|https://*:*)
    fail "CORS_ORIGIN must be an exact HTTPS production origin"
    ;;
  https://?*) ;;
  *) fail "CORS_ORIGIN must be an exact HTTPS production origin" ;;
esac

primary_site=${SITE_ADDRESS:-}
primary_site=${primary_site%%,*}
primary_site=${primary_site%% *}
[ -n "$primary_site" ] || fail "SITE_ADDRESS must name the primary production host"
[ "$cors_origin" = "https://$primary_site" ] || fail "CORS_ORIGIN must match the primary SITE_ADDRESS host"

if [ "${CONTENT_PUBLISHING_ENABLED:-false}" = "true" ]; then
  [ -n "${MEDIA_S3_BUCKET:-}" ] || fail "MEDIA_S3_BUCKET is required while publishing is enabled"
fi

if [ "$#" -gt 0 ]; then
  exec "$@"
fi
