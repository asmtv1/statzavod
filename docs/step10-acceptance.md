# Step 10 — independent acceptance record

**Date:** 2026-08-26  
**Scope:** workspace RBAC, lifecycle/audit, management UI and creator portal.

## Result

Local acceptance passed. No new implementation defect was found during this
step. The checks below are local/test-environment evidence, not a production
deployment or live-provider certification.

## Backend and database

- A disposable PostgreSQL 18.4 instance applied migrations `00001` through
  `00023` for fresh, upgrade, and lifecycle-upgrade paths; the migration suite
  also exercised the compatible down/up boundary `23 → 19 → 20`.
- Live integration coverage verified OWNER/MANAGER/CREATOR company A/B scope,
  creator-portal IDOR resistance, context switching, old management endpoint
  denial, archive/restore/retention purge, OAuth/sync/archive races, shared
  grants, self-delete/workspace cascade and audit fail-closed/transactional
  behaviour.
- `go test -race -count=1 ./...`, `go vet ./...`, and `git diff --check`
  completed successfully.

## Excel export

- The creator export fixture was generated as an `.xlsx`, reopened with
  Excelize, and checked for two sheets, KPI values, date range, typed numeric
  cells, Russian/English labels, empty-state copy, and a single active creator
  profile.
- Portal integration tests also prove that a caller-provided `creatorId` does
  not alter the export scope.

## Frontend and accessibility

- `npm run test` passed: 4 files, 14 tests.
- `npm run build` and `npm run check:i18n` passed; i18n validated 552 Cyrillic
  keys.
- `npm run lint` has 0 errors and 12 pre-existing hook/Fast Refresh warnings.
- Browser smoke checks passed at 1440×900, 1024×768, and 390×844: meaningful
  public DOM, no horizontal overflow, and no relevant console errors.
- Direct signed-out navigation to `/app/team`, `/app/audit`, and
  `/app/my/accounts` redirects to `/login` without rendering protected data.
- RTL/source review covers route guards, user+context scoped query keys,
  403-driven cache removal/refetch, dialog naming/focus trap/Escape/focus
  restore, reduced motion, and secret cleanup on profile/scope change.

## Baseline mismatch ledger

| Baseline point | Rendered evidence | Result |
| --- | --- | --- |
| Palette | Public desktop/tablet/mobile renders keep graphite, gold primary and red/mint semantic roles. | Match |
| Shell / responsive layout | `scrollWidth === clientWidth` at 1440, 1024 and 390 px; mobile collapses without a horizontal trap. | Match |
| Typography and density | Public and guarded screens retain the Inter hierarchy and compact existing-system controls. | Match |
| Panels / actions | Existing rounded graphite panels, translucent borders and gold primary actions are preserved. | Match |
| Tables / portal content | CSS review confirms minimum-width tables scroll inside their own container and chart panels collapse at tablet/mobile. | Match |
| Dialogs | Shared dialog hook and RTL interaction tests confirm labelled modal semantics, trap, Escape and focus return. | Match |

## Remaining external gates

- No production deployment, production data migration, or real OAuth-provider
  callback was performed.
- Authenticated browser flows were not run because test password entry is
  sensitive-data transmission and needs action-time user confirmation. Their
  role, scope, lifecycle and UI contracts are covered by live HTTP integration
  and RTL tests.
