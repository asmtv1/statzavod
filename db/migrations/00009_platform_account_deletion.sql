-- +goose Up
ALTER TABLE creator_account_assignments
  DROP CONSTRAINT creator_account_assignments_platform_account_id_fkey,
  ADD CONSTRAINT creator_account_assignments_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id) ON DELETE CASCADE;

ALTER TABLE publications
  DROP CONSTRAINT publications_platform_account_id_fkey,
  ADD CONSTRAINT publications_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id) ON DELETE CASCADE;

ALTER TABLE content_group_members
  DROP CONSTRAINT content_group_members_publication_id_fkey,
  ADD CONSTRAINT content_group_members_publication_id_fkey
    FOREIGN KEY (publication_id) REFERENCES publications(id) ON DELETE CASCADE;

ALTER TABLE content_match_suggestions
  DROP CONSTRAINT content_match_suggestions_publication_a_id_fkey,
  ADD CONSTRAINT content_match_suggestions_publication_a_id_fkey
    FOREIGN KEY (publication_a_id) REFERENCES publications(id) ON DELETE CASCADE,
  DROP CONSTRAINT content_match_suggestions_publication_b_id_fkey,
  ADD CONSTRAINT content_match_suggestions_publication_b_id_fkey
    FOREIGN KEY (publication_b_id) REFERENCES publications(id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE content_match_suggestions
  DROP CONSTRAINT content_match_suggestions_publication_a_id_fkey,
  ADD CONSTRAINT content_match_suggestions_publication_a_id_fkey
    FOREIGN KEY (publication_a_id) REFERENCES publications(id),
  DROP CONSTRAINT content_match_suggestions_publication_b_id_fkey,
  ADD CONSTRAINT content_match_suggestions_publication_b_id_fkey
    FOREIGN KEY (publication_b_id) REFERENCES publications(id);

ALTER TABLE content_group_members
  DROP CONSTRAINT content_group_members_publication_id_fkey,
  ADD CONSTRAINT content_group_members_publication_id_fkey
    FOREIGN KEY (publication_id) REFERENCES publications(id);

ALTER TABLE publications
  DROP CONSTRAINT publications_platform_account_id_fkey,
  ADD CONSTRAINT publications_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id);

ALTER TABLE creator_account_assignments
  DROP CONSTRAINT creator_account_assignments_platform_account_id_fkey,
  ADD CONSTRAINT creator_account_assignments_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id);
