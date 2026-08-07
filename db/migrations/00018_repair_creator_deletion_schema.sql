-- +goose Up
-- Repair production databases that recorded migration 17 before its final schema
-- changes were present.  Keep this idempotent: healthy databases may already
-- contain the column and cascade constraints.
ALTER TABLE creators
  ADD COLUMN IF NOT EXISTS created_by uuid REFERENCES users(id) ON DELETE SET NULL;

UPDATE creators AS creator
SET created_by = creator_create.actor_id
FROM (
  SELECT DISTINCT ON (organization_id, entity_id)
    organization_id,
    entity_id,
    actor_id
  FROM audit_logs
  WHERE action = 'CREATE'
    AND entity_type = 'CREATOR'
    AND entity_id IS NOT NULL
    AND actor_id IS NOT NULL
  ORDER BY organization_id, entity_id, created_at, id
) AS creator_create
WHERE creator.created_by IS NULL
  AND creator.id = creator_create.entity_id
  AND creator.organization_id = creator_create.organization_id;

ALTER TABLE creator_contacts
  DROP CONSTRAINT IF EXISTS creator_contacts_creator_id_fkey,
  ADD CONSTRAINT creator_contacts_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;

ALTER TABLE creator_account_assignments
  DROP CONSTRAINT IF EXISTS creator_account_assignments_creator_id_fkey,
  ADD CONSTRAINT creator_account_assignments_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;

ALTER TABLE publications
  DROP CONSTRAINT IF EXISTS publications_creator_id_fkey,
  ADD CONSTRAINT publications_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;

ALTER TABLE content_groups
  DROP CONSTRAINT IF EXISTS content_groups_creator_id_fkey,
  ADD CONSTRAINT content_groups_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;

ALTER TABLE content_match_suggestions
  DROP CONSTRAINT IF EXISTS content_match_suggestions_creator_id_fkey,
  ADD CONSTRAINT content_match_suggestions_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;

ALTER TABLE oauth_states
  DROP CONSTRAINT IF EXISTS oauth_states_creator_id_fkey,
  ADD CONSTRAINT oauth_states_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;

-- +goose Down
-- This is a forward-only production repair.  The prior state is not reliably
-- knowable because version 17 had already been marked applied.
