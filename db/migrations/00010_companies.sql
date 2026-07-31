-- +goose Up
CREATE TABLE companies (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);

CREATE INDEX companies_organization_idx ON companies (organization_id, name)
  WHERE archived_at IS NULL;
CREATE UNIQUE INDEX companies_active_name_idx ON companies (organization_id, lower(name))
  WHERE archived_at IS NULL;

ALTER TABLE creators ADD COLUMN company_id uuid REFERENCES companies(id) ON DELETE SET NULL;
CREATE INDEX creators_company_idx ON creators (company_id);

-- +goose Down
DROP INDEX IF EXISTS creators_company_idx;
ALTER TABLE creators DROP COLUMN IF EXISTS company_id;
DROP TABLE IF EXISTS companies;
