-- +goose Up
ALTER TABLE company_vk_accounts
  DROP CONSTRAINT IF EXISTS company_vk_accounts_access_check;

-- +goose Down
ALTER TABLE company_vk_accounts
  ADD CONSTRAINT company_vk_accounts_access_check
  CHECK (login_ciphertext IS NOT NULL OR phone_ciphertext IS NOT NULL) NOT VALID;
