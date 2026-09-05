-- +goose Up
-- A provider deletion receipt may outlive its platform account so Meta can
-- poll its status, but it must never outlive every workspace whose data it
-- described. Remove legacy unscoped receipts: their tenant association cannot
-- be reconstructed safely after the account FK was set to NULL.
DELETE FROM platform_data_deletion_requests WHERE organization_id IS NULL;

-- Receipts whose account has already gone keep only polling state. Hash the
-- legacy confirmation secret and erase provider/user identifiers.
UPDATE platform_data_deletion_requests
SET confirmation_code='md5:' || md5(confirmation_code),
    external_id='',
    error_message=NULL
WHERE platform_account_id IS NULL
  AND confirmation_code NOT LIKE 'sha256:%'
  AND confirmation_code NOT LIKE 'md5:%';

ALTER TABLE platform_data_deletion_requests
  DROP CONSTRAINT platform_data_deletion_requests_organization_id_fkey,
  ADD CONSTRAINT platform_data_deletion_requests_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;

CREATE TABLE platform_data_deletion_request_workspaces (
  confirmation_code text NOT NULL
    REFERENCES platform_data_deletion_requests(confirmation_code)
    ON UPDATE CASCADE ON DELETE CASCADE,
  organization_id uuid NOT NULL
    REFERENCES organizations(id) ON DELETE CASCADE,
  PRIMARY KEY (confirmation_code, organization_id)
);

INSERT INTO platform_data_deletion_request_workspaces(confirmation_code,organization_id)
SELECT confirmation_code,organization_id
FROM platform_data_deletion_requests
WHERE organization_id IS NOT NULL;

-- Multi-copy Instagram requests have no single organization_id. Their join
-- rows preserve every workspace association and this trigger removes the
-- PII-bearing receipt before any associated workspace is deleted.
-- +goose StatementBegin
CREATE FUNCTION delete_platform_deletion_requests_for_workspace() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM platform_data_deletion_requests request
  WHERE EXISTS (
    SELECT 1 FROM platform_data_deletion_request_workspaces scope
    WHERE scope.confirmation_code=request.confirmation_code
      AND scope.organization_id=OLD.id
  );
  RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER organizations_delete_platform_deletion_requests
  BEFORE DELETE ON organizations
  FOR EACH ROW EXECUTE FUNCTION delete_platform_deletion_requests_for_workspace();

-- Provider status polling may outlive an account/company purge, but only an
-- anonymized receipt is allowed to do so.
-- +goose StatementBegin
CREATE FUNCTION anonymize_platform_deletion_requests_for_account() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE platform_data_deletion_requests
  SET confirmation_code=CASE
        WHEN confirmation_code LIKE 'sha256:%' OR confirmation_code LIKE 'md5:%'
          THEN confirmation_code
        ELSE 'md5:' || md5(confirmation_code)
      END,
      external_id='',
      error_message=NULL
  WHERE platform_account_id=OLD.id;
  RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER platform_accounts_anonymize_deletion_requests
  BEFORE DELETE ON platform_accounts
  FOR EACH ROW EXECUTE FUNCTION anonymize_platform_deletion_requests_for_account();

-- +goose Down
DROP TRIGGER IF EXISTS platform_accounts_anonymize_deletion_requests ON platform_accounts;
DROP FUNCTION IF EXISTS anonymize_platform_deletion_requests_for_account();
DROP TRIGGER IF EXISTS organizations_delete_platform_deletion_requests ON organizations;
DROP FUNCTION IF EXISTS delete_platform_deletion_requests_for_workspace();
DROP TABLE IF EXISTS platform_data_deletion_request_workspaces;

ALTER TABLE platform_data_deletion_requests
  DROP CONSTRAINT platform_data_deletion_requests_organization_id_fkey,
  ADD CONSTRAINT platform_data_deletion_requests_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE SET NULL;
