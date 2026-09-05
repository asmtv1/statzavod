-- +goose Up
-- Expand-compatible lifecycle and audit infrastructure. Existing lifecycle
-- columns and status values remain valid for the previous application version.

CREATE TYPE workspace_lifecycle_state AS ENUM ('ACTIVE', 'DELETING');

CREATE TABLE operational_receipts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  operation text NOT NULL CHECK (operation IN ('WORKSPACE_DELETE', 'COMPANY_PURGE')),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'COMPLETE')),
  requested_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
  CHECK ((status = 'PENDING' AND completed_at IS NULL)
      OR (status = 'COMPLETE' AND completed_at IS NOT NULL))
);

ALTER TABLE organizations
  ADD COLUMN lifecycle_state workspace_lifecycle_state NOT NULL DEFAULT 'ACTIVE',
  ADD COLUMN deleting_at timestamptz,
  ADD COLUMN deletion_receipt_id uuid REFERENCES operational_receipts(id) ON DELETE SET NULL,
  ADD COLUMN delete_lease_owner text,
  ADD COLUMN delete_lease_until timestamptz,
  ADD CONSTRAINT organizations_deletion_lifecycle_check CHECK (
    (lifecycle_state = 'ACTIVE' AND deleting_at IS NULL AND deletion_receipt_id IS NULL)
    OR
    (lifecycle_state = 'DELETING' AND deleting_at IS NOT NULL AND deletion_receipt_id IS NOT NULL)
  ),
  ADD CONSTRAINT organizations_delete_lease_pair_check CHECK (
    (delete_lease_owner IS NULL) = (delete_lease_until IS NULL)
  );

CREATE INDEX organizations_deleting_queue_idx
  ON organizations (delete_lease_until, deleting_at, id)
  WHERE lifecycle_state = 'DELETING';

ALTER TABLE companies
  ADD COLUMN purge_lease_owner text,
  ADD COLUMN purge_lease_until timestamptz,
  ADD CONSTRAINT companies_purge_lease_pair_check CHECK (
    (purge_lease_owner IS NULL) = (purge_lease_until IS NULL)
  );

DROP INDEX companies_lifecycle_purge_idx;
CREATE INDEX companies_lifecycle_purge_idx
  ON companies (purge_at, purge_lease_until, id)
  WHERE lifecycle_state = 'ARCHIVED';

ALTER TABLE oauth_connections
  ADD COLUMN suspended_for_company_archive boolean NOT NULL DEFAULT false;

ALTER TABLE sync_targets
  ADD COLUMN suspended_for_company_archive boolean NOT NULL DEFAULT false;

ALTER TABLE audit_logs
  ADD COLUMN company_id uuid,
  ADD COLUMN endpoint text,
  ADD COLUMN http_method text,
  ADD COLUMN response_status integer,
  ADD CONSTRAINT audit_logs_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
  ADD CONSTRAINT audit_logs_http_status_check CHECK (
    response_status IS NULL OR response_status BETWEEN 100 AND 599
  ),
  ADD CONSTRAINT audit_logs_company_workspace_fkey
    FOREIGN KEY (company_id, organization_id)
    REFERENCES companies (id, organization_id)
    ON DELETE SET NULL (company_id);

CREATE INDEX audit_logs_workspace_created_idx
  ON audit_logs (organization_id, created_at DESC, id DESC);
CREATE INDEX audit_logs_workspace_action_created_idx
  ON audit_logs (organization_id, action, created_at DESC, id DESC);
CREATE INDEX audit_logs_workspace_company_created_idx
  ON audit_logs (organization_id, company_id, created_at DESC, id DESC)
  WHERE company_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS audit_logs_workspace_company_created_idx;
DROP INDEX IF EXISTS audit_logs_workspace_action_created_idx;
DROP INDEX IF EXISTS audit_logs_workspace_created_idx;
ALTER TABLE audit_logs
  DROP CONSTRAINT IF EXISTS audit_logs_company_workspace_fkey,
  DROP CONSTRAINT IF EXISTS audit_logs_http_status_check,
  DROP CONSTRAINT IF EXISTS audit_logs_metadata_object_check,
  DROP COLUMN IF EXISTS response_status,
  DROP COLUMN IF EXISTS http_method,
  DROP COLUMN IF EXISTS endpoint,
  DROP COLUMN IF EXISTS company_id;

ALTER TABLE sync_targets DROP COLUMN IF EXISTS suspended_for_company_archive;
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS suspended_for_company_archive;

DROP INDEX IF EXISTS companies_lifecycle_purge_idx;
CREATE INDEX companies_lifecycle_purge_idx
  ON companies (lifecycle_state, purge_at)
  WHERE lifecycle_state = 'ARCHIVED';
ALTER TABLE companies
  DROP CONSTRAINT IF EXISTS companies_purge_lease_pair_check,
  DROP COLUMN IF EXISTS purge_lease_until,
  DROP COLUMN IF EXISTS purge_lease_owner;

DROP INDEX IF EXISTS organizations_deleting_queue_idx;
ALTER TABLE organizations
  DROP CONSTRAINT IF EXISTS organizations_delete_lease_pair_check,
  DROP CONSTRAINT IF EXISTS organizations_deletion_lifecycle_check,
  DROP COLUMN IF EXISTS delete_lease_until,
  DROP COLUMN IF EXISTS delete_lease_owner,
  DROP COLUMN IF EXISTS deletion_receipt_id,
  DROP COLUMN IF EXISTS deleting_at,
  DROP COLUMN IF EXISTS lifecycle_state;

DROP TABLE IF EXISTS operational_receipts;
DROP TYPE IF EXISTS workspace_lifecycle_state;
