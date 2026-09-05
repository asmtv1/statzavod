-- +goose Up
-- Session cookies are credentials. Any change to an account identity or secret
-- invalidates every outstanding credential for that account.
-- +goose StatementBegin
CREATE FUNCTION revoke_sessions_on_user_security_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.email IS DISTINCT FROM OLD.email
     OR NEW.password_hash IS DISTINCT FROM OLD.password_hash
     OR NEW.status IS DISTINCT FROM OLD.status THEN
    UPDATE sessions
    SET revoked_at = COALESCE(revoked_at, now()),
        active_company_id = NULL,
        active_creator_id = NULL
    WHERE user_id = NEW.id AND revoked_at IS NULL;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_revoke_sessions_on_security_change
  AFTER UPDATE OF email, password_hash, status ON users
  FOR EACH ROW EXECUTE FUNCTION revoke_sessions_on_user_security_change();

-- +goose Down
DROP TRIGGER IF EXISTS users_revoke_sessions_on_security_change ON users;
DROP FUNCTION IF EXISTS revoke_sessions_on_user_security_change();
