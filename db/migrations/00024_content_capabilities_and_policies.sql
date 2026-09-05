-- +goose Up
-- Content permissions are additive. Existing manager assignments stay valid;
-- owners continue to have every capability through the authorization layer.
ALTER TYPE manager_permission ADD VALUE IF NOT EXISTS 'CONTENT_VIEW';
ALTER TYPE manager_permission ADD VALUE IF NOT EXISTS 'CONTENT_CREATE';
ALTER TYPE manager_permission ADD VALUE IF NOT EXISTS 'CONTENT_EDIT';
ALTER TYPE manager_permission ADD VALUE IF NOT EXISTS 'CONTENT_APPROVE';
ALTER TYPE manager_permission ADD VALUE IF NOT EXISTS 'CONTENT_PUBLISH';
ALTER TYPE manager_permission ADD VALUE IF NOT EXISTS 'CONTENT_DELETE';

CREATE TABLE creator_content_approval_policies (
  creator_id uuid PRIMARY KEY,
  organization_id uuid NOT NULL,
  company_id uuid NOT NULL,
  publish_requires_approval boolean NOT NULL DEFAULT false,
  edit_requires_approval boolean NOT NULL DEFAULT false,
  delete_requires_approval boolean NOT NULL DEFAULT false,
  updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (creator_id, organization_id) REFERENCES creators(id, organization_id),
  FOREIGN KEY (company_id, organization_id) REFERENCES companies(id, organization_id)
);
CREATE INDEX creator_content_approval_policies_company_idx
  ON creator_content_approval_policies (organization_id, company_id, creator_id);

-- +goose StatementBegin
CREATE FUNCTION validate_creator_content_policy_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE creator_company uuid;
BEGIN
  SELECT company_id INTO creator_company FROM creators
  WHERE id=NEW.creator_id AND organization_id=NEW.organization_id;
  IF creator_company IS NULL OR creator_company <> NEW.company_id THEN
    RAISE EXCEPTION 'content policy resources belong to another company' USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER creator_content_approval_policies_validate_scope
  BEFORE INSERT OR UPDATE OF creator_id, organization_id, company_id
  ON creator_content_approval_policies
  FOR EACH ROW EXECUTE FUNCTION validate_creator_content_policy_scope();

-- +goose Down
DROP TRIGGER IF EXISTS creator_content_approval_policies_validate_scope ON creator_content_approval_policies;
DROP FUNCTION IF EXISTS validate_creator_content_policy_scope();
DROP TABLE IF EXISTS creator_content_approval_policies;
-- PostgreSQL enum labels are intentionally retained: removing an enum label is
-- unsafe and would rewrite existing RBAC data. Application rollback ignores the
-- additive labels.
