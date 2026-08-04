-- +goose Up
CREATE TABLE oauth_connection_invitations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  creator_id uuid NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
  provider_key text NOT NULL CHECK (provider_key IN ('instagram')),
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  revoked_at timestamptz,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX oauth_connection_invitations_active_idx
  ON oauth_connection_invitations (organization_id, creator_id, provider_key, expires_at DESC)
  WHERE consumed_at IS NULL AND revoked_at IS NULL;

ALTER TABLE oauth_states
  ADD COLUMN connection_invitation_id uuid
    REFERENCES oauth_connection_invitations(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE oauth_states DROP COLUMN IF EXISTS connection_invitation_id;
DROP TABLE IF EXISTS oauth_connection_invitations;
