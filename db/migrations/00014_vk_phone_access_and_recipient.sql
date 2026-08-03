-- +goose Up
ALTER TABLE company_vk_accounts
  ALTER COLUMN login_ciphertext DROP NOT NULL,
  ALTER COLUMN login_nonce DROP NOT NULL,
  ALTER COLUMN password_ciphertext DROP NOT NULL,
  ALTER COLUMN password_nonce DROP NOT NULL,
  ADD CONSTRAINT company_vk_accounts_login_pair_check CHECK ((login_ciphertext IS NULL) = (login_nonce IS NULL)),
  ADD CONSTRAINT company_vk_accounts_password_pair_check CHECK ((password_ciphertext IS NULL) = (password_nonce IS NULL)),
  ADD CONSTRAINT company_vk_accounts_access_check CHECK (login_ciphertext IS NOT NULL OR phone_ciphertext IS NOT NULL);

ALTER TABLE creator_vk_assignments
  ADD COLUMN recipient_account_url text NOT NULL DEFAULT '',
  ADD CONSTRAINT creator_vk_assignments_recipient_account_url_check CHECK (recipient_account_url = '' OR recipient_account_url ~ '^https://(vk\.ru|vk\.com)/[^/]+/?$');

-- +goose Down
ALTER TABLE creator_vk_assignments
  DROP CONSTRAINT IF EXISTS creator_vk_assignments_recipient_account_url_check,
  DROP COLUMN IF EXISTS recipient_account_url;

ALTER TABLE company_vk_accounts
  DROP CONSTRAINT IF EXISTS company_vk_accounts_access_check,
  DROP CONSTRAINT IF EXISTS company_vk_accounts_password_pair_check,
  DROP CONSTRAINT IF EXISTS company_vk_accounts_login_pair_check,
  ALTER COLUMN login_ciphertext SET NOT NULL,
  ALTER COLUMN login_nonce SET NOT NULL,
  ALTER COLUMN password_ciphertext SET NOT NULL,
  ALTER COLUMN password_nonce SET NOT NULL;
