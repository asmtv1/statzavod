# Company scope and creator portal audit

Audit date: 2026-08-11. Scope: every raw SQL/API resource family present after migrations `00020`–`00021`.

## Authorization invariants

- `OWNER` may use all-company mode only for workspace analytics, lists and exports. Selecting a company constrains resource access to that company.
- `MANAGER` requests require an active, currently assigned company. Permissions are read from that company's assignment on every authenticated request.
- `CREATOR` is rejected by every legacy management endpoint. Creator-portal resource IDs are derived from `sessions.active_creator_id`, `sessions.active_company_id`, the current `login_user_id`, and the current workspace.
- Cross-workspace and cross-company resource IDs return `404` after visibility resolution. A visible resource without the requested manager permission returns `403`.
- Archived companies and profiles cannot be loaded into an authenticated active context.

## Raw SQL/resource ledger

| Resource family | Read scope | Mutation scope | Verification |
| --- | --- | --- | --- |
| Companies | `organization_id`, active lifecycle, plus `company_id` for selected OWNER/MANAGER contexts | OWNER-only company resource check | live two-workspace/two-company matrix; scoped list test |
| Creators and contacts | creator workspace + active company join; list predicates use active `company_id` | creator resource is resolved before permission; creator company is immutable | live list/IDOR/mutation matrix |
| Credentials and history | management reads are beneath a visible creator; portal reads also require active profile + `login_user_id` + company | `CREDENTIAL_EDIT` / `SECRET_REVEAL`; portal has reveal only and audit must succeed before output | live own/cross-profile/cross-user reveal test |
| Company VK and creator VK assignments | company account, creator and platform account must share workspace and company | `CREDENTIAL_EDIT`, `SOCIAL_CONNECT`, or `SECRET_REVEAL` on the resolved company | cross-company VK assignment test; creator mutation denial |
| OAuth states/selections/connections | state carries workspace, initiator and creator/company owner; callback rechecks active company assignment and permission | `SOCIAL_CONNECT`; CREATOR cannot authorize or select accounts | revoked-permission callback test; selection company-scope test |
| Platform accounts and assignments | account workspace/company must match its creator and active management context | account resource resolution plus `SOCIAL_CONNECT`/`SYNC_MANAGE` | platform-account IDOR and creator sync/OAuth denial |
| Integrations and synchronization | aggregate/list queries include `platform_accounts.company_id`; sync health includes `sync_targets.company_id`; worker jobs carry organization and company validated by migration triggers | account resource check plus `SYNC_MANAGE`; workers use scoped targets | live manager integration aggregate excludes company B |
| Publications and content groups | publication/group joins traverse creator → company and apply active context | group creation resolves creator then requires `CREATOR_EDIT` | live manager analytics/list scope; content-group IDOR test |
| Analytics | summary/timeseries/publications use scoped creator CTE/join; creator detail analytics is permissioned beneath a visible creator | read-only, `STATS_VIEW` | live manager A reports 111 views while OWNER all reports 1110 |
| Management Excel export | requested IDs are resolved in workspace and active company before data reads | `STATS_EXPORT`; OWNER all may intentionally combine companies | IDOR export test; Excelize reopen tests |
| Creator portal and own Excel | no creator ID parameter; active creator is always taken from validated session context | portal exposes no card/credential/social mutation, sync, OAuth, disconnect or purge route | live profile switch, arbitrary query ignored, active-only XLSX reopen |
| Provider privacy callbacks | provider external ID lookup intentionally finds all local copies, then each deletion is executed with its stored `organization_id` and account ID | signed provider callback only, not an authenticated management endpoint | existing Instagram signed-request/deletion tests |

## Error and workbook audit

- SQL query, `Scan`, row iteration, audit insert, JSON metadata and transaction errors are checked in production paths; no SQL mutation uses a blank assignment.
- Export rows and KPI are completely loaded before workbook creation.
- Every Excelize operation that returns an error is checked. XLSX is serialized to memory and the workbook is closed before HTTP headers are written.
- Excelize reopen tests cover RU/EN sheets and labels, KPI numeric cells, date cells, a populated period, and an empty publications report.
- Independent `@oai/artifact-tool` import/inspect/render verification found two expected sheets, correct typed values, no formula-error cells and no clipped content.
