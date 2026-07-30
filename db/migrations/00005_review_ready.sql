-- +goose Up
CREATE TABLE organizations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO organizations (name, slug) VALUES ('Statzavod', 'statzavod');

CREATE TABLE organization_memberships (
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role user_role NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organization_id, user_id)
);

INSERT INTO organization_memberships (organization_id, user_id, role)
SELECT o.id, u.id, u.role FROM organizations o CROSS JOIN users u WHERE o.slug = 'statzavod';

CREATE TABLE user_invitations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email text NOT NULL CHECK (email = lower(email)),
  role user_role NOT NULL DEFAULT 'VIEWER',
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  created_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_invitations_active_idx ON user_invitations (email, expires_at) WHERE accepted_at IS NULL;

ALTER TABLE creators ADD COLUMN organization_id uuid REFERENCES organizations(id);
ALTER TABLE platform_accounts ADD COLUMN organization_id uuid REFERENCES organizations(id);
ALTER TABLE publications ADD COLUMN organization_id uuid REFERENCES organizations(id);
ALTER TABLE oauth_connections ADD COLUMN organization_id uuid REFERENCES organizations(id);
ALTER TABLE oauth_states ADD COLUMN organization_id uuid REFERENCES organizations(id);
ALTER TABLE oauth_states ADD COLUMN initiated_by uuid REFERENCES users(id);
ALTER TABLE sync_targets ADD COLUMN organization_id uuid REFERENCES organizations(id);
ALTER TABLE audit_logs ADD COLUMN organization_id uuid REFERENCES organizations(id);

UPDATE creators SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');
UPDATE platform_accounts SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');
UPDATE publications SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');
UPDATE oauth_connections SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');
UPDATE oauth_states SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');
UPDATE sync_targets SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');
UPDATE audit_logs SET organization_id = (SELECT id FROM organizations WHERE slug = 'statzavod');

ALTER TABLE creators ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE platform_accounts ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE publications ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE oauth_connections ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE oauth_states ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE sync_targets ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE audit_logs ALTER COLUMN organization_id SET NOT NULL;

ALTER TABLE platform_accounts DROP CONSTRAINT platform_accounts_platform_external_id_key;
ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_org_platform_external_id_key UNIQUE (organization_id, platform, external_id);
ALTER TABLE publications DROP CONSTRAINT publications_platform_external_id_key;
ALTER TABLE publications ADD CONSTRAINT publications_org_platform_external_id_key UNIQUE (organization_id, platform, external_id);
CREATE UNIQUE INDEX sync_targets_active_operation_idx ON sync_targets (organization_id, target_id, operation) WHERE status='ACTIVE';
ALTER TABLE oauth_states ALTER COLUMN pkce_verifier_ciphertext DROP NOT NULL;
ALTER TABLE oauth_connections ADD COLUMN access_token_nonce bytea;
ALTER TABLE oauth_connections ADD COLUMN refresh_token_nonce bytea;
UPDATE oauth_connections SET access_token_nonce = nonce WHERE access_token_nonce IS NULL;
ALTER TABLE oauth_connections ALTER COLUMN access_token_nonce SET NOT NULL;

ALTER TABLE oauth_connections ADD COLUMN disconnect_requested_at timestamptz;
ALTER TABLE oauth_connections ADD COLUMN purge_after timestamptz;

-- +goose Down
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS purge_after;
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS disconnect_requested_at;
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS refresh_token_nonce;
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS access_token_nonce;
ALTER TABLE oauth_states ALTER COLUMN pkce_verifier_ciphertext SET NOT NULL;
ALTER TABLE platform_accounts DROP CONSTRAINT IF EXISTS platform_accounts_org_platform_external_id_key;
ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_platform_external_id_key UNIQUE (platform, external_id);
DROP INDEX IF EXISTS sync_targets_active_operation_idx;
ALTER TABLE publications DROP CONSTRAINT IF EXISTS publications_org_platform_external_id_key;
ALTER TABLE publications ADD CONSTRAINT publications_platform_external_id_key UNIQUE (platform, external_id);
ALTER TABLE audit_logs DROP COLUMN IF EXISTS organization_id;
ALTER TABLE sync_targets DROP COLUMN IF EXISTS organization_id;
ALTER TABLE oauth_states DROP COLUMN IF EXISTS initiated_by;
ALTER TABLE oauth_states DROP COLUMN IF EXISTS organization_id;
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS organization_id;
ALTER TABLE publications DROP COLUMN IF EXISTS organization_id;
ALTER TABLE platform_accounts DROP COLUMN IF EXISTS organization_id;
ALTER TABLE creators DROP COLUMN IF EXISTS organization_id;
DROP TABLE IF EXISTS user_invitations;
DROP TABLE IF EXISTS organization_memberships;
DROP TABLE IF EXISTS organizations;
