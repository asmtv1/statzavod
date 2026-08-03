-- +goose Up
ALTER TABLE creators
  DROP CONSTRAINT creators_work_comment_check,
  DROP CONSTRAINT creators_work_status_check,
  ADD CONSTRAINT creators_work_status_check CHECK (work_status IN ('OK', 'NEEDS_ATTENTION', 'IN_PROGRESS')),
  ADD CONSTRAINT creators_work_comment_check CHECK (work_status = 'OK' OR btrim(work_comment) <> '');

CREATE TABLE creator_history_events (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id),
  creator_id uuid NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
  actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
  block text NOT NULL CHECK (block IN ('PROFILE', 'WORK', 'CREDENTIALS')),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE creator_history_changes (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  event_id uuid NOT NULL REFERENCES creator_history_events(id) ON DELETE CASCADE,
  section text,
  field_key text NOT NULL,
  is_secret boolean NOT NULL DEFAULT false,
  old_present boolean NOT NULL DEFAULT false,
  new_present boolean NOT NULL DEFAULT false,
  old_value text,
  new_value text,
  old_value_ciphertext bytea,
  old_value_nonce bytea,
  new_value_ciphertext bytea,
  new_value_nonce bytea,
  CHECK (
    (is_secret AND old_value IS NULL AND new_value IS NULL)
    OR
    (NOT is_secret AND old_value_ciphertext IS NULL AND old_value_nonce IS NULL AND new_value_ciphertext IS NULL AND new_value_nonce IS NULL)
  )
);

CREATE INDEX creator_history_events_creator_idx
  ON creator_history_events (creator_id, block, created_at DESC);
CREATE INDEX creator_history_changes_event_idx
  ON creator_history_changes (event_id);

-- +goose Down
DROP TABLE IF EXISTS creator_history_changes;
DROP TABLE IF EXISTS creator_history_events;

UPDATE creators
SET work_status = 'NEEDS_ATTENTION'
WHERE work_status = 'IN_PROGRESS';

ALTER TABLE creators
  DROP CONSTRAINT creators_work_comment_check,
  DROP CONSTRAINT creators_work_status_check,
  ADD CONSTRAINT creators_work_status_check CHECK (work_status IN ('OK', 'NEEDS_ATTENTION')),
  ADD CONSTRAINT creators_work_comment_check CHECK (work_status = 'OK' OR btrim(work_comment) <> '');
