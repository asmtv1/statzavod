-- +goose Up
CREATE TABLE oauth_states (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id),
  platform platform NOT NULL,
  state_hash bytea NOT NULL UNIQUE,
  pkce_verifier_ciphertext bytea NOT NULL,
  nonce bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_states_active_idx ON oauth_states(state_hash,expires_at) WHERE consumed_at IS NULL;
-- +goose Down
DROP TABLE IF EXISTS oauth_states;
