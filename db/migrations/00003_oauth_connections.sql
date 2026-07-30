-- +goose Up
CREATE TABLE oauth_connections (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  platform_account_id uuid NOT NULL UNIQUE REFERENCES platform_accounts(id) ON DELETE CASCADE,
  access_token_ciphertext bytea NOT NULL,
  refresh_token_ciphertext bytea,
  nonce bytea NOT NULL,
  key_version integer NOT NULL DEFAULT 1,
  scopes text[] NOT NULL DEFAULT '{}',
  expires_at timestamptz,
  last_refreshed_at timestamptz,
  status text NOT NULL DEFAULT 'ACTIVE',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
DROP TABLE IF EXISTS oauth_connections;
