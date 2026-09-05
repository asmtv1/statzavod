#!/usr/bin/env sh
set -eu

: "${MEDIA_S3_BUCKET:?set MEDIA_S3_BUCKET}"
: "${MEDIA_S3_ACCESS_KEY:?set MEDIA_S3_ACCESS_KEY}"
: "${MEDIA_S3_SECRET_KEY:?set MEDIA_S3_SECRET_KEY}"

endpoint=${MEDIA_S3_ENDPOINT:-https://storage.yandexcloud.net}
[ "$endpoint" = "https://storage.yandexcloud.net" ] || {
  printf '%s\n' "This script is restricted to the official Yandex Object Storage endpoint." >&2
  exit 1
}

command -v aws >/dev/null 2>&1 || {
  printf '%s\n' "aws CLI is required." >&2
  exit 1
}

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
policy="$script_dir/yandex-object-storage-cors.json"

# This changes only the bucket CORS document. It does not grant public-read,
# change the bucket ACL, or expose object keys.
AWS_ACCESS_KEY_ID=$MEDIA_S3_ACCESS_KEY \
AWS_SECRET_ACCESS_KEY=$MEDIA_S3_SECRET_KEY \
AWS_DEFAULT_REGION=${MEDIA_S3_REGION:-ru-central1} \
  aws --endpoint-url "$endpoint" s3api put-bucket-cors \
    --bucket "$MEDIA_S3_BUCKET" \
    --cors-configuration "file://$policy"

AWS_ACCESS_KEY_ID=$MEDIA_S3_ACCESS_KEY \
AWS_SECRET_ACCESS_KEY=$MEDIA_S3_SECRET_KEY \
AWS_DEFAULT_REGION=${MEDIA_S3_REGION:-ru-central1} \
  aws --endpoint-url "$endpoint" s3api get-bucket-cors \
    --bucket "$MEDIA_S3_BUCKET"
