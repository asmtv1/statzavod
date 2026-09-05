# Platform review packages

This is an evidence template, not an approval claim. A reviewer must fill the
date, environment, account, and result fields after a real test account run.
Never place access tokens, refresh tokens, signed URLs, private object keys, or
real creator personal data in this file or in screenshots.

## Shared evidence record

Fill one record for each provider and release candidate:

| Field | Value to fill before submission |
| --- | --- |
| Release/build | `<git revision or image digest>` |
| Environment | `<sandbox/staging/production URL>` |
| Test workspace/company | `<non-production identifier>` |
| Test account | `<provider handle/channel; no password or token>` |
| Test video | `<synthetic or consented fixture; checksum only>` |
| Run timestamp | `<UTC>` |
| Result | `PASS / FAIL / BLOCKED` |
| External review state | `not submitted / submitted / approved / changes requested` |
| Evidence links | `<redacted screenshot/video paths>` |

Required screenshots use the actual deployed URL and exact route. Suggested
stable names are:

`publishing-account-selection.png`, `publishing-platform-options.png`,
`publishing-preview-consent.png`, `publishing-scheduled.png`,
`publishing-partial-result.png`, `publishing-reauth.png`,
`publishing-history-retry-failed.png`, `publishing-mobile-agenda.png`,
`publishing-audit-redacted.png`.

## TikTok Content Posting API Direct Post

Official references: [Direct Post API](https://developers.tiktok.com/docs/en/content-posting-api-reference-direct-post),
[Direct Post getting started](https://developers.tiktok.com/docs/en/content-posting-api-get-started),
[Content Sharing Guidelines](https://developers.tiktok.com/docs/en/content-sharing-guidelines),
[App Review Guidelines](https://developers.tiktok.com/docs/en/app-review-guidelines),
and [post status](https://developers.tiktok.com/docs/en/content-posting-api-reference-get-video-status).

Requested publishing scope: `video.publish`. The app must show current creator
information and available privacy values before posting; the user explicitly
confirms sending the video and Music Usage Confirmation. Demonstrate these
scenarios at `/app/publishing`:

1. Select a TikTok account and verify the visible nickname matches the
   connected test account.
2. Choose a privacy value returned by creator info; verify no privacy value is
   silently defaulted.
3. Show preview, caption, consent, and publish confirmation; capture
   `publishing-preview-consent.png`.
4. Submit a synthetic video and show the target state transition and provider
   status. Record the provider operation ID only in a redacted test log.
5. Revoke the scope, reconnect the same account, and capture
   `publishing-reauth.png`.

Evidence fields: creator-info response redacted, requested scope, selected
privacy, account nickname, explicit consent text, source video checksum,
provider result, and whether the app is still subject to TikTok private-view
limits. Unaudited clients must be described as unaudited; do not claim public
visibility or approval before TikTok confirms it.

## Meta Instagram Reels

Official references: [Instagram API with Facebook Login](https://developers.facebook.com/docs/instagram-api/get-started),
[Content Publishing](https://developers.facebook.com/docs/instagram-api/guides/content-publishing),
[Instagram Login](https://developers.facebook.com/docs/instagram-platform/instagram-api-with-instagram-login),
and [Meta App Review](https://developers.facebook.com/docs/apps/review/).

The tested account must be a Professional Business or Creator account. Record
which login mode was used and request only the matching scope:
`instagram_business_content_publish` for Instagram Login or
`instagram_content_publish` for Facebook Login for Business. Demonstrate at
`/app/publishing`:

1. Select the Professional account and show account identity and scope state.
2. Preview the Reels caption and provider-safe video URL flow without exposing
   a signed URL; capture `publishing-preview-consent.png`.
3. Publish one test Reel and capture the resulting permalink in a redacted
   test report. Do not put tokens or URLs with credentials into screenshots.
4. Simulate an expired/revoked scope, show `WAITING_FOR_REAUTH`, reconnect the
   same account, and capture `publishing-reauth.png`.

Meta submission evidence must include a short end-to-end screen recording,
privacy policy, terms, data deletion URL, exact requested permissions, test
credentials supplied through Meta’s secure review channel, and the answer for
why each permission is needed. Approval remains an external Meta decision.

## YouTube Data API Shorts

Official references: [YouTube authentication](https://developers.google.com/youtube/documentation/authentication),
[OAuth for web server applications](https://developers.google.com/youtube/v3/guides/auth/server-side-web-apps),
[YouTube API Services Terms](https://developers.google.com/youtube/terms/api-services-terms-of-service),
and [videos.insert](https://developers.google.com/youtube/v3/docs/videos/insert).

Requested upload scope: `https://www.googleapis.com/auth/youtube.upload`.
The YouTube flow uses OAuth user consent and resumable `videos.insert`; Shorts
is a format/metadata convention, not a separate upload API. Demonstrate at
`/app/publishing`:

1. Connect the test channel and record the channel identity without a token.
2. Set title, description, category, privacy, made-for-kids, and subscriber
   notification values explicitly; capture `publishing-platform-options.png`.
3. Upload the fixture through the resumable flow, interrupt once, resume the
   same session, poll processing, and record the final video URL.
4. Disconnect/reconnect the same Google account and capture
   `publishing-reauth.png`.

Verification materials must include the OAuth consent screen, privacy policy,
terms, data deletion instructions, project ownership, requested scope
justification, demo recording, and a test account/channel that the reviewer
can use. Google verification and quota approval are external gates.

## VK Video fallback

Official reference: [VK API video methods](https://dev.vk.com/method/video).
The first release documents and tests VK Video upload/post behavior. It does
not promise VK Clips and must not label the UI, marketing, or review evidence
as Clips support. Demonstrate at `/app/publishing`:

1. Select a VK Video account/community with the required publish readiness.
2. Upload a consented fixture and record the returned VK Video URL.
3. Exercise a provider error and retry only the failed target.
4. Capture `publishing-partial-result.png` if sibling targets have different
   outcomes.

Record the actual VK scopes, account type, API version, provider method, and
result URL in the shared evidence record. VK access and policy decisions remain
external to this repository.

## Evidence review gate

Before enabling a provider flag, an operator must attach the completed record,
redacted screenshots, and provider test result to the release ticket. A local
fixture test or a green build proves implementation behavior only; it does not
prove TikTok audit, Meta App Review, Google verification, or VK production
access.

