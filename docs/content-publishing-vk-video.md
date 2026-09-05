# VK Видео fallback

StatZavod publishes the VK target as VK Видео through the `video.save` API,
streams the frozen media object from S3 to the returned `upload_url`, and can
optionally create a `wall.post` attachment. This adapter does not create VK
Clips and must not be described or labelled as Clips.

The target snapshot must include an explicit `owner` (`user` or `community`)
and numeric `ownerId` (community IDs are sent as negative VK owner IDs). The
VK connection needs the `video` scope; wall-posting additionally requires the
account's wall permission. Missing permissions are actionable: reconnect the
account and grant the requested permissions.

Provider operation IDs are durable and phase-qualified. A crash after the
save intent is fail-closed because repeating `video.save` could allocate a
second video. Upload URL expiry is the one safe recovery: the adapter calls
`video.save` with the existing `video_id` to obtain a fresh upload URL, then
continues streaming. A wall-post intent is also fail-closed after an uncertain
response, preventing duplicate posts. Finalization stores the VK owner/video
ID and `https://vk.ru/video{owner_id}_{video_id}` URL on the publish target.

The feature is disabled unless both `CONTENT_PUBLISHING_ENABLED=true` and
`CONTENT_VK_ENABLED=true`. Provider response bodies and tokens are never
returned to callers; user-facing failures are redacted into reconnect,
permission, transient, or permanent actions.

Production readiness still requires an external VK provider gate covering user
and community accounts, actual permission grants, upload URL expiry, and wall
post behavior. Local httptest and race/vet checks do not certify that gate.
