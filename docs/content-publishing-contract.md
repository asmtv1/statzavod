# Content publishing contract

The publishing feature is deliberately separate from `publications`.  The latter
remains the analytics projection of an externally published entity; a successful
target upserts its provider result into that projection.

## First-release scope

One immutable revision contains one short vertical video and one or more target
accounts on TikTok, Instagram, YouTube, or **VK Video**.  VK Clips are not
claimed: the VK adapter uses the documented video upload/post workflow.  Images,
carousels, recurring posts, and edits/deletions of external posts are out of
scope.

## State and authority

`content_items` has DRAFT, PENDING_APPROVAL, APPROVED, SCHEDULED, PUBLISHING,
PARTIALLY_PUBLISHED, PUBLISHED, FAILED, and CANCELLED aggregate states.  Target
states are stored independently, so one provider failure never rolls back a
successful provider.  The aggregate is derived from targets, never accepted from
the client.

All draft mutations require `If-Match: <revision>` and create a new immutable
revision.  A mutation supersedes outstanding approval and marks every old
in-flight target for cancellation; a worker observes that marker while holding
its target lock before finalizing.  Copy and state-command POSTs require an
`Idempotency-Key`; its canonical method, route and JSON body are bound to the
key, so a same request replays its original response and a different body gets
`409`.  Notification read POSTs are already semantically idempotent and do not
require a key. Multipart abort (`DELETE`) is likewise a naturally idempotent
terminal decision; multipart creation and completion use the command-key
contract.

The capabilities are `CONTENT_VIEW`, `CONTENT_CREATE`, `CONTENT_EDIT`,
`CONTENT_APPROVE`, `CONTENT_PUBLISH`, and `CONTENT_DELETE`.  A creator may only
operate their active profile.  A manager is limited to assigned companies; an
owner can use any company in the workspace.  The author cannot approve their own
request.  Creator approval policy affects only actions initiated by that creator;
manager and owner actions require their own capability but bypass that policy.

## Time, media, and providers

The API receives RFC3339 timestamps with the Europe/Moscow offset and persists
UTC.  The browser stores only upload metadata/progress/object URLs, never video
bytes in persistent client state.  A target points to exactly one media asset.
Validation must verify the file signature, actual MIME, checksum and vertical
dimensions before it becomes READY.  Public/provider delivery is only through a
time-limited HTTPS URL; bucket keys are never returned.

Jobs are claimed with `FOR UPDATE SKIP LOCKED`.  The DB lock is released before
provider I/O, then company/target/revision lifecycle is locked again before the
result is committed.  Retryable failures use bounded backoff; auth failures wait
for reconnect and do not consume retry budget.  The provider operation ID is
persisted before retrying an uncertain result.

Immediately before a publish, schedule, or retry job is inserted, every eligible
target receives an atomic immutable `sent_snapshot`: revision, caption,
hashtags, platform options, media ID/object hash and account identity/mode.
Adapters send this snapshot rather than rereading draft fields. Retrying a target
retains its original snapshot.

Feature flags: `CONTENT_PUBLISHING_ENABLED`, `CONTENT_TIKTOK_ENABLED`,
`CONTENT_INSTAGRAM_ENABLED`, `CONTENT_YOUTUBE_ENABLED`, and
`CONTENT_VK_ENABLED`.  A disabled feature rejects new publish commands while
leaving existing analytics intact.

The complete operational activation, rollback, provider-outage, reauth and
cleanup procedure is in [content-publishing-runbook.md](content-publishing-runbook.md).

## Connection readiness and reconnect

Publishing readiness is evaluated independently for every target account. The
connection must be active and must include the provider's publishing scope:
`instagram_business_content_publish` for Instagram Login,
`instagram_content_publish` for Facebook Login for Business, `video.publish`
for TikTok, `youtube.upload` for YouTube, and `video` (with `wall` and
`groups` requested for the documented VK Video/community flow) for VK.
Read-only historical connections remain valid for analytics but are not
publish-ready. A missing scope or revoked connection blocks only that target as
`WAITING_FOR_REAUTH`; it does not cancel sibling targets or consume retry
budget. The readiness response exposes only the missing scopes and the
`RECONNECT_PLATFORM_ACCOUNT` action, never a token.

A reconnect must prove the same provider account identity before a token is
bound. A successful verified reconnect may resume only waiting targets for that
same account; it must not resume a different account merely because it belongs
to the same creator or company. Grant, reconnect, and revoke audit records use
only platform, account IDs, and scope names.

## Instagram Reels

Instagram publishing is limited to Professional Business or Creator accounts.
Instagram Login requires `instagram_business_content_publish`; Facebook Login
for Business requires `instagram_content_publish`. The adapter creates a Reels
container from the immutable target caption and provider-safe `video_url`, then
stores phase-qualified operation IDs (`ig:container:<id>` and `ig:media:<id>`).
It polls container processing before one `media_publish` call, and finally reads
the published media ID and permalink. Provider payloads are normalized before
they reach target errors or attempts; tokens and signed URLs are never retained.
`media_publish` is preceded by a durable `ig:publish-dispatched:<container>`
intent. Meta's container status response does not provide a durable published
media ID that can prove recovery after an interrupted publish response, so a
recovered dispatched intent fails closed for investigation instead of issuing a
second publish request.

## YouTube Shorts

YouTube Shorts use the standard YouTube Data API `videos.insert` resumable
upload. The immutable snapshot supplies title, description, category, privacy,
made-for-kids, and subscriber-notification options. The resumable session URI is
stored as a phase-qualified operation ID (`yt:session:<opaque>`), queried and
resumed after interruptions, and replaced by `yt:video:<id>` once the upload
returns a video ID. Processing is polled before exposing the final Shorts URL.
There is no separate Shorts publishing API. The adapter is gated by
`CONTENT_PUBLISHING_ENABLED` and `CONTENT_YOUTUBE_ENABLED`; OAuth consent and
Google project verification remain external activation gates.
