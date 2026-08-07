-- +goose Up
CREATE TABLE oauth_account_selections (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  creator_id uuid NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
  initiated_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform platform NOT NULL,
  payload_ciphertext bytea NOT NULL,
  nonce bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (platform = 'INSTAGRAM')
);

CREATE INDEX oauth_account_selections_active_idx
  ON oauth_account_selections (id, organization_id, creator_id, expires_at)
  WHERE consumed_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS oauth_account_selections;
