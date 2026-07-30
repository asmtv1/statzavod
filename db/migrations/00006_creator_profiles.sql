-- +goose Up
ALTER TABLE creators ADD COLUMN telegram_username text NOT NULL DEFAULT '';

CREATE TABLE creator_credentials (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
  section text NOT NULL,
  field_key text NOT NULL,
  is_secret boolean NOT NULL DEFAULT false,
  value_ciphertext bytea NOT NULL,
  value_nonce bytea NOT NULL,
  updated_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (creator_id, section, field_key)
);
CREATE INDEX creator_credentials_creator_idx ON creator_credentials (creator_id, section);

-- +goose Down
DROP TABLE IF EXISTS creator_credentials;
ALTER TABLE creators DROP COLUMN IF EXISTS telegram_username;
