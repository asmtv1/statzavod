-- +goose Up
ALTER TABLE company_vk_accounts
  ADD COLUMN platform_account_id uuid UNIQUE REFERENCES platform_accounts(id) ON DELETE SET NULL;

ALTER TABLE oauth_states
  ALTER COLUMN creator_id DROP NOT NULL,
  ADD COLUMN company_vk_account_id uuid REFERENCES company_vk_accounts(id) ON DELETE CASCADE,
  ADD CONSTRAINT oauth_states_owner_check CHECK (
    (creator_id IS NOT NULL AND company_vk_account_id IS NULL)
    OR (creator_id IS NULL AND company_vk_account_id IS NOT NULL)
  );

-- +goose Down
DELETE FROM oauth_states WHERE company_vk_account_id IS NOT NULL;
ALTER TABLE oauth_states
  DROP CONSTRAINT IF EXISTS oauth_states_owner_check,
  DROP COLUMN IF EXISTS company_vk_account_id,
  ALTER COLUMN creator_id SET NOT NULL;
ALTER TABLE company_vk_accounts DROP COLUMN IF EXISTS platform_account_id;
