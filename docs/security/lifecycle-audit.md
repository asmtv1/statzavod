# Lifecycle and audit contract

## Retention and deletion

- `DELETE /api/v1/companies/{id}` is OWNER-only. It sets `archived_at` to the server clock and `purge_at` to exactly 90 days later. No company child is detached.
- Archived companies are absent from normal OWNER, MANAGER, and CREATOR scope. OWNER uses `GET /api/v1/companies/archive` and may restore with `POST /api/v1/companies/{id}/restore` strictly before `purge_at`.
- Archival pauses only active OAuth connections and sync targets and marks the reason. Restore resumes only rows with that marker. Manual pauses and reauthorization errors remain unchanged.
- There is no authenticated permanent-company-delete endpoint. The lifecycle worker claims due rows with `FOR UPDATE SKIP LOCKED`, records a lease, performs bounded provider revocation outside the transaction, and commits the local cascade only while it still owns the lease.
- `DELETE /api/v1/users/me` requires the current password and current email. A co-owner removes only their own account. The sole owner atomically changes the workspace to `DELETING` and revokes every session; the worker then deletes the tenant without the 90-day company delay.
- Deleting a creator removes the linked login account when its last profile is deleted, credentials, contacts, OAuth state, assignments, and all mutable content state. A minimal archived creator tombstone with fixed non-PII labels is retained only to preserve successful publication/audit evidence and to fence durable media cleanup; it has no login linkage or original profile fields.
- The workspace audit cascades with the workspace. Only an anonymous `operational_receipts` row remains; it contains operation, status, timestamps, and non-PII outcome metadata.

## Endpoint audit coverage

The executable exact endpoint-to-action table is `auditEndpointCoverage` in `internal/transport/http/audit.go`. `TestAuthenticatedEndpointAuditCoverageTable` walks every registered route and fails for a missing or stale entry.

| Endpoint patterns | Audit action |
| --- | --- |
| `GET /api/v1/auth/me`, `POST /api/v1/auth/context` | `HTTP_<METHOD>_API_V1_AUTH_*` |
| `/api/v1/companies*`, `/api/v1/company-vk-accounts*` reads and mutations | `HTTP_<METHOD>_API_V1_COMPANIES_*` or `HTTP_<METHOD>_API_V1_COMPANY_VK_ACCOUNTS_*` |
| `/api/v1/creators*` reads, history, credential reveal, and mutations | `HTTP_<METHOD>_API_V1_CREATORS_*` |
| `/api/v1/analytics*`, `/api/v1/publications`, `/api/v1/exports` | `HTTP_<METHOD>_API_V1_<ROUTE>` |
| `/api/v1/integrations`, `/api/v1/sync/health`, platform account sync/pause/resume/delete | `HTTP_<METHOD>_API_V1_<ROUTE>` |
| `/api/v1/creator-portal*`, including reveal and export | `HTTP_<METHOD>_API_V1_CREATOR_PORTAL_*` |
| `/api/v1/users*`, `/api/v1/audit` | `HTTP_<METHOD>_API_V1_USERS_*` or `HTTP_GET_API_V1_AUDIT` |
| lifecycle purge, platform sync, OAuth refresh | `SYSTEM_PURGE_*`, `SYSTEM_SYNC_*`, `SYSTEM_OAUTH_REFRESH` |

Only successful authenticated business responses are centrally written. The response is buffered until the audit insert succeeds, so reveal and export fail closed. Login, logout, anonymous callbacks/receipts, `/healthz`, and `/readyz` are excluded.

Audit metadata is constructed from a fixed empty/aggregate map. Request bodies, passwords, password hashes, cookies, OAuth tokens, token ciphertext/nonces, and revealed credential values are never copied. Sync metadata contains counts only; provider error text is not persisted.
