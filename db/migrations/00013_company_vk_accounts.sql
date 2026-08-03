-- +goose Up
CREATE TABLE company_vk_accounts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  company_id uuid NOT NULL UNIQUE REFERENCES companies(id) ON DELETE CASCADE,
  login_ciphertext bytea NOT NULL,
  login_nonce bytea NOT NULL,
  password_ciphertext bytea NOT NULL,
  password_nonce bytea NOT NULL,
  phone_ciphertext bytea,
  phone_nonce bytea,
  created_by uuid REFERENCES users(id),
  updated_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((phone_ciphertext IS NULL) = (phone_nonce IS NULL))
);
CREATE INDEX company_vk_accounts_organization_idx ON company_vk_accounts (organization_id);

CREATE TABLE creator_vk_assignments (
  creator_id uuid PRIMARY KEY REFERENCES creators(id) ON DELETE CASCADE,
  company_vk_account_id uuid NOT NULL REFERENCES company_vk_accounts(id) ON DELETE CASCADE,
  community_url text NOT NULL,
  updated_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (community_url ~ '^https://(vk\.ru|vk\.com)/[^/]+/?$')
);
CREATE INDEX creator_vk_assignments_account_idx ON creator_vk_assignments (company_vk_account_id);

-- +goose Down
DROP TABLE IF EXISTS creator_vk_assignments;
DROP TABLE IF EXISTS company_vk_accounts;
