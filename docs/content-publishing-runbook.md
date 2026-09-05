# Content publishing runbook

## Activation and rollback

1. Back up PostgreSQL and record the currently applied goose migration.
2. Apply the expand-only migrations while `CONTENT_PUBLISHING_ENABLED=false`.
3. Deploy the API and worker image; it contains `ffmpeg` and `ffprobe`.
4. Check `/readyz`, storage credentials, the HTTPS `MEDIA_PUBLIC_BASE_URL`, OAuth callbacks, and worker logs. Verify `MEDIA_UPLOAD_ORIGIN` exactly equals the origin of `MEDIA_S3_ENDPOINT`; startup intentionally fails when they differ.
5. Enable the global flag for one internal company, then one provider flag at a time: TikTok, Instagram, YouTube, VK Video.

The flags are independent operational controls. `CONTENT_PUBLISHING_ENABLED`
is the global kill switch. Each provider additionally requires its own flag:
`CONTENT_TIKTOK_ENABLED`, `CONTENT_INSTAGRAM_ENABLED`,
`CONTENT_YOUTUBE_ENABLED`, or `CONTENT_VK_ENABLED`. Enable one provider at a
time and confirm its queue, result, latency, and error metrics before enabling
the next. A provider outage should not require disabling healthy providers.

To roll back, turn off the global/provider flags first. Do not delete media,
audit evidence, or external posts. Cooperatively cancel jobs that have not
called a provider, investigate `provider_operation_id` before any retry, and
only then roll back the application image. Schema remains in place.

## Operational response

- **Oldest due job rising:** inspect `content_publish_jobs` in READY or
  RETRY_SCHEDULED state and the worker health; never run a second manual
  publish for a target with a provider operation ID.
- **Reauth:** reconnect the same account. Confirm provider identity matches the
  saved external ID; a different account must not resume an existing target.
- **Provider outage:** disable only that provider flag. Existing successes stay
  successful; retry only affected targets after recovery.
- **Stuck processing:** inspect the latest immutable attempt and redacted
  provider payload. Poll provider status before retrying creation.
- **Instagram Reels:** keep `CONTENT_INSTAGRAM_ENABLED=false` until Meta App
  Review is approved. For a stuck `ig:container:<id>`, poll its status; for an
  `ig:media:<id>`, fetch that media and its permalink. Do not manually create a
  new container for a target with either operation ID. Confirm the connection
  uses the correct scope for its login mode before asking the user to reconnect.
  A stuck `ig:publish-dispatched:<container>` is deliberately fail-closed: do
  not retry `media_publish`; investigate the container and account manually.
- **Cleanup:** abort upload sessions after 24 hours; delete only unreferenced
  rejected/temporary assets after seven days. Never delete READY media used by
  a revision or a published target.

## Metrics and alerts

Prometheus runs only on the private Compose network and scrapes
`http://api:8080/metrics` every 30 seconds. Caddy deliberately returns 404 for
the public `/metrics` path. The collector must expose only bounded labels
(`platform`, `result`, histogram `le`, and sanitized `error_class`); tenant,
workspace, account, content, target, job, attempt, operation IDs, object keys,
URLs, response bodies, and secrets are forbidden as metric labels.

Use these metric families during rollout and incident response:

- `statzavod_content_publish_queue_depth` and
  `statzavod_content_publish_oldest_due_seconds` show due work and worker lag.
- `statzavod_content_publish_results_total{result}` covers success, partial,
  and failure outcomes; `statzavod_content_publish_retries_total{platform}`
  identifies retry pressure.
- `statzavod_content_provider_latency_seconds` is a bounded histogram used for
  provider p95 latency. `statzavod_content_provider_errors_total` exposes only
  a sanitized, finite `error_class`, never the raw provider error.
- `statzavod_content_reauth_connections` and
  `statzavod_content_reauth_oldest_seconds` track blocked connections and age.
- `statzavod_content_transcoding_failures_total`,
  `statzavod_content_orphan_uploads`, and `statzavod_content_cleanup_errors`
  cover the media pipeline and cleanup backlog.

Alert rules live in `deploy/content-publishing-alerts.yml`. Before deployment,
CI validates both the production Compose model and the Prometheus configuration
with `promtool`. After deployment, confirm Prometheus target `statzavod-api` is
UP and no rule reports an evaluation error. Prometheus data is retained for
`PROMETHEUS_RETENTION_TIME` (15 days by default); snapshots and long-term remote
storage are separate operator responsibilities.

Alert response order:

1. For queue age/depth, verify the worker is running and the applicable global
   and provider flags are enabled; inspect due jobs without exposing IDs in
   labels or chat.
2. For provider latency/errors, disable only the affected provider flag, then
   inspect immutable attempts and existing provider operation IDs before retry.
3. For partial results, leave successful targets untouched and investigate only
   failed or reauth-blocked targets.
4. For reauth, reconnect the same external identity. Never resume against a
   newly selected account.
5. For media cleanup/transcoding, use normal fenced worker tasks and preserve
   READY or referenced objects.

## Security and review evidence

Videos are private objects. The API may issue only time-limited provider-safe
HTTPS delivery URLs; it must never expose bucket credentials, object keys,
OAuth tokens, or signed URLs in audit metadata/logs. Capture the exact UI flow
and test-account result for TikTok Content Posting audit, Meta App Review, and
YouTube OAuth verification. UI and API call VK target **VK Video**, never VK
Clips: the first release uses `video.save` and placement of the uploaded video.

### Private Object Storage browser uploads

Keep the bucket private; CORS is not an ACL and must never be paired with
public-read. Before enabling publishing, review
`deploy/yandex-object-storage-cors.json`, then explicitly apply it with
`deploy/apply-yandex-object-storage-cors.sh` from an operator shell that has
short-lived bucket-administration credentials. The policy permits only the two
production app origins, the PUT/HEAD/GET methods used by multipart validation,
the bounded set of upload/checksum headers, and exposes only `ETag` for the
browser's multipart completion request. Its preflight cache is 900 seconds.

After application, run an OPTIONS preflight using the production app Origin
and `Access-Control-Request-Method: PUT`; require that the response returns that
exact origin and `ETag` is exposed. Repeat with an unapproved Origin and with an
unapproved request header; both must be denied. Confirm the main HTML CSP
`connect-src` contains only the exact `MEDIA_UPLOAD_ORIGIN`, never `*` or an
S3 hostname wildcard. The automated equivalent is
`npm run e2e -- production-upload-cors.spec.ts` in `web/`.

For YouTube, keep both publishing flags disabled until OAuth consent and Google
project verification are complete. A `yt:session:` operation is resumable and
must be status-queried before retrying bytes; once it becomes `yt:video:`, poll
processing and never initialize a second upload for that target.

## Worker lease recovery and duplicate investigation

Each publishing job has an owner, execution number, and `lease_expires_at`.
The worker renews the lease during provider I/O. A second worker may reclaim a
stale `RUNNING` job only after the lease has expired and the row is claimed
under the same `FOR UPDATE SKIP LOCKED` discipline. The old worker cannot
finalize after reclaim: finalization is fenced by worker ID and execution
number. A reclaimed job creates an immutable attempt record and is marked
cancelled or failed according to whether the provider boundary is known.

When a process dies during provider I/O, first inspect the latest attempt and
`provider_operation_id`. For a known operation, poll the provider and reuse
the result. For an unknown operation, follow the adapter's safe retry policy;
never click Publish twice manually. Record the target, platform, time window,
operation phase, and redacted provider error in the incident ticket. Do not
copy access tokens, signed URLs, object keys, or full provider responses into
the ticket.

## Environment and runtime contract

The API and worker use the same image family and the same applied migration
set. The migration image must finish migrations before the API or worker is
marked ready. Startup validation must fail before job processing when any
enabled provider lacks its OAuth client credentials, redirect URL, or required
scope configuration; it must also fail when publishing is enabled without a
private media bucket, delivery token key, HTTPS delivery base URL, or exact
upload origin. `deploy/validate-production-config.sh` is the preflight check.

| Area | Production setting | Check |
| --- | --- | --- |
| Global/provider flags | `CONTENT_PUBLISHING_ENABLED` plus one of `CONTENT_TIKTOK_ENABLED`, `CONTENT_INSTAGRAM_ENABLED`, `CONTENT_YOUTUBE_ENABLED`, `CONTENT_VK_ENABLED` | Start with all false; enable one provider per canary company |
| Private media | `MEDIA_S3_ENDPOINT`, `MEDIA_S3_REGION`, `MEDIA_S3_BUCKET`, `MEDIA_S3_ACCESS_KEY`, `MEDIA_S3_SECRET_KEY` | Bucket denies public-read; credentials are injected, never committed |
| Browser upload | `MEDIA_UPLOAD_ORIGIN` exactly equals the storage API origin | Run the production CORS E2E and denied-origin preflight |
| Provider delivery | `MEDIA_PUBLIC_BASE_URL=https://...` and `MEDIA_DELIVERY_TOKEN_KEY` | Host is HTTPS and is not the S3 API host; URL is time limited |
| Public/domain | `PUBLIC_BASE_URL`, `CORS_ORIGIN`, `SITE_ADDRESS`, `MEDIA_SITE_ADDRESS` | Exact origins, TLS certificates, Caddy CSP and callback URLs agree |
| Media runtime | `ffprobe` and `ffmpeg` in the worker image | `ffprobe -version` and a synthetic MP4 validation during image smoke |
| Metrics | private Prometheus network, `PROMETHEUS_RETENTION_TIME` | `/metrics` is not public; promtool validates alert rules |

The worker must have bounded scratch space and one media validation slot per
process. Alert on disk pressure before ffprobe/ffmpeg failures become a queue
backlog. Keep deployment, migration, and worker image digests in the release
record.

## Provider outage, cleanup, and rollback

Disable only the affected provider flag during an outage. Leave successful
targets and healthy provider queues running. After recovery, poll every target
with a provider operation ID before allowing a new create request. Cleanup
errors are retried through the fenced cleanup task; an operator must not delete
objects directly from the bucket while a revision or published target can
reference them.

To roll back an application image, disable the global publishing flag, wait for
workers to stop claiming new work, inspect running leases and provider
operations, then deploy the previously recorded image. Keep migrations applied
because the rollback image may still need to read audit and attempt history.
Re-enable flags only after `/readyz`, queue age, media cleanup, and provider
polling checks pass. External provider posts are never rolled back by deleting
local rows.

## Release checklist

- [ ] Record the release revision/image digests and back up PostgreSQL.
- [ ] Apply and verify the expand-only migrations on a clean database and on a
      copy of the current schema; record the applied migration version.
- [ ] Run startup configuration validation with all publishing flags disabled;
      verify required OAuth, media, domain, and delivery-key settings are
      present without printing their values.
- [ ] Verify API, worker, and migration images use the same release contract
      and that the worker image contains compatible `ffmpeg` and `ffprobe`.
- [ ] Verify the private bucket policy, exact production CORS origins, denied
      origin/header preflight, and Caddy CSP `connect-src` allowlist.
- [ ] Verify `MEDIA_PUBLIC_BASE_URL` is HTTPS and is not the storage API host;
      confirm provider-safe URLs contain no bucket keys or credentials.
- [ ] Validate Prometheus rules with `promtool`; confirm private scraping,
      bounded labels, queue/oldest-due, result, retry, latency, reauth,
      transcoding, orphan, cleanup, and provider-error metrics.
- [ ] Run Go tests, race tests, vet, migration tests, web typecheck/lint/build,
      i18n checks, Playwright publishing regression, and upload CORS E2E.
- [ ] Complete browser smoke for `/app/publishing`, `/app/audit`, RU/EN legal
      pages, responsive layout, account selection, preview/consent, schedule,
      partial result, reauth, and retry failed only.
- [ ] Run fake-provider end-to-end checks for each enabled provider and verify
      successful targets appear in analytics without duplicating external IDs.
- [ ] Enable `CONTENT_PUBLISHING_ENABLED=false` in production first; enable
      exactly one provider flag for one internal canary company, monitor queue,
      result, latency, and errors, then expand gradually.

## Rollback checklist

- [ ] Disable `CONTENT_PUBLISHING_ENABLED` and the affected provider flag(s);
      record the time and keep healthy provider flags unchanged.
- [ ] Stop new worker claims and inspect due jobs, active leases,
      `execution_count`, immutable attempts, cancellation markers, and every
      `provider_operation_id` in the incident window.
- [ ] Poll providers for known operations before any retry; do not manually
      create a second post for a target whose provider boundary is uncertain.
- [ ] Preserve audit records, successful external publications, and referenced
      READY media; do not delete rows or bucket objects as a rollback shortcut.
- [ ] Confirm lease reclaim and fencing prevent an old worker from finalizing
      after the image is drained; wait for or explicitly cancel stale workers.
- [ ] Deploy the recorded previous API/worker image while retaining the
      migrations required to read attempts, leases, audits, and cleanup tasks.
- [ ] Verify `/readyz`, database connectivity, worker startup validation,
      storage credentials, CORS/CSP, ffmpeg/ffprobe, and Prometheus target
      health on the rolled-back image.
- [ ] Run queue, reauth, provider-poll, cleanup, and duplicate-investigation
      smoke checks; confirm temporary/orphan cleanup remains fenced and does not
      remove referenced media.
- [ ] Keep publishing disabled until the incident has a documented cause and
      provider state is reconciled; re-enable only the global flag and one
      provider canary after the release checklist is re-run.
