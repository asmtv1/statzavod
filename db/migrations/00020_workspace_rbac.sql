-- +goose Up
-- Expand-only RBAC and company-scope migration.  users.role and
-- organization_memberships.role intentionally remain available to the legacy API.

CREATE TYPE membership_role AS ENUM ('OWNER', 'MANAGER', 'CREATOR');
CREATE TYPE manager_permission AS ENUM (
  'STATS_VIEW',
  'STATS_EXPORT',
  'CREATOR_CREATE',
  'CREATOR_EDIT',
  'CREATOR_ARCHIVE',
  'CREATOR_DELETE',
  'CREATOR_ACCOUNT_MANAGE',
  'SOCIAL_CONNECT',
  'SYNC_MANAGE',
  'CREDENTIAL_EDIT',
  'SECRET_REVEAL'
);
CREATE TYPE company_lifecycle_state AS ENUM ('ACTIVE', 'ARCHIVED');

ALTER TABLE organization_memberships
  ADD COLUMN membership_role membership_role;

UPDATE organization_memberships
SET membership_role = CASE role
  WHEN 'ADMIN' THEN 'OWNER'::membership_role
  WHEN 'ANALYST' THEN 'MANAGER'::membership_role
  WHEN 'VIEWER' THEN 'MANAGER'::membership_role
END;

ALTER TABLE organization_memberships
  ALTER COLUMN membership_role SET NOT NULL,
  ADD CONSTRAINT organization_memberships_workspace_role_key
    UNIQUE (organization_id, user_id, membership_role),
  ADD CONSTRAINT organization_memberships_one_workspace_key UNIQUE (user_id);

CREATE INDEX organization_memberships_workspace_role_idx
  ON organization_memberships (organization_id, membership_role, user_id);

COMMENT ON COLUMN organization_memberships.membership_role IS
  'Authorization source of truth. The legacy role column is retained temporarily for rollback compatibility.';

-- +goose StatementBegin
CREATE FUNCTION synchronize_membership_roles() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.membership_role IS NULL THEN
    NEW.membership_role := CASE NEW.role
      WHEN 'ADMIN' THEN 'OWNER'::membership_role
      WHEN 'ANALYST' THEN 'MANAGER'::membership_role
      WHEN 'VIEWER' THEN 'MANAGER'::membership_role
    END;
  ELSIF NEW.role IS NULL THEN
    NEW.role := CASE NEW.membership_role
      WHEN 'OWNER' THEN 'ADMIN'::user_role
      WHEN 'MANAGER' THEN 'VIEWER'::user_role
      WHEN 'CREATOR' THEN 'VIEWER'::user_role
    END;
  ELSIF TG_OP = 'UPDATE'
        AND NEW.role IS DISTINCT FROM OLD.role
        AND NEW.membership_role IS NOT DISTINCT FROM OLD.membership_role THEN
    NEW.membership_role := CASE NEW.role
      WHEN 'ADMIN' THEN 'OWNER'::membership_role
      WHEN 'ANALYST' THEN 'MANAGER'::membership_role
      WHEN 'VIEWER' THEN 'MANAGER'::membership_role
    END;
  ELSIF TG_OP = 'UPDATE'
        AND NEW.membership_role IS DISTINCT FROM OLD.membership_role
        AND NEW.role IS NOT DISTINCT FROM OLD.role THEN
    NEW.role := CASE NEW.membership_role
      WHEN 'OWNER' THEN 'ADMIN'::user_role
      WHEN 'MANAGER' THEN 'VIEWER'::user_role
      WHEN 'CREATOR' THEN 'VIEWER'::user_role
    END;
  END IF;

  IF NEW.membership_role = 'OWNER' AND NEW.role <> 'ADMIN'
     OR NEW.membership_role = 'MANAGER' AND NEW.role NOT IN ('ANALYST', 'VIEWER')
     OR NEW.membership_role = 'CREATOR' AND NEW.role <> 'VIEWER' THEN
    RAISE EXCEPTION 'legacy and membership roles are inconsistent'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.membership_role <> 'CREATOR' AND EXISTS (
    SELECT 1 FROM creators creator
    WHERE creator.organization_id = NEW.organization_id
      AND creator.login_user_id = NEW.user_id
  ) THEN
    RAISE EXCEPTION 'creator membership still owns creator profiles'
      USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER organization_memberships_synchronize_roles
  BEFORE INSERT OR UPDATE OF role, membership_role ON organization_memberships
  FOR EACH ROW EXECUTE FUNCTION synchronize_membership_roles();

-- A user/workspace membership is the tenant identity. Moving that row would
-- silently re-scope nullable actor references and existing sessions.
-- +goose StatementBegin
CREATE FUNCTION prevent_membership_identity_move() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.organization_id IS DISTINCT FROM OLD.organization_id
     OR NEW.user_id IS DISTINCT FROM OLD.user_id THEN
    RAISE EXCEPTION 'membership workspace and user are immutable'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER organization_memberships_identity_immutable
  BEFORE UPDATE OF organization_id, user_id ON organization_memberships
  FOR EACH ROW EXECUTE FUNCTION prevent_membership_identity_move();

ALTER TABLE companies
  ADD COLUMN lifecycle_state company_lifecycle_state NOT NULL DEFAULT 'ACTIVE',
  ADD COLUMN purge_at timestamptz,
  ADD CONSTRAINT companies_id_organization_key UNIQUE (id, organization_id);

UPDATE companies
SET lifecycle_state = 'ARCHIVED',
    purge_at = COALESCE(purge_at, archived_at + interval '90 days')
WHERE archived_at IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION normalize_company_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.archived_at IS NULL THEN
    NEW.lifecycle_state := 'ACTIVE';
    NEW.purge_at := NULL;
  ELSE
    NEW.lifecycle_state := 'ARCHIVED';
    NEW.purge_at := COALESCE(NEW.purge_at, NEW.archived_at + interval '90 days');
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER companies_normalize_lifecycle
  BEFORE INSERT OR UPDATE OF archived_at, lifecycle_state, purge_at ON companies
  FOR EACH ROW EXECUTE FUNCTION normalize_company_lifecycle();

ALTER TABLE companies
  ADD CONSTRAINT companies_lifecycle_check CHECK (
    (lifecycle_state = 'ACTIVE' AND archived_at IS NULL AND purge_at IS NULL)
    OR
    (lifecycle_state = 'ARCHIVED' AND archived_at IS NOT NULL AND purge_at IS NOT NULL AND purge_at >= archived_at)
  );

CREATE INDEX companies_lifecycle_purge_idx
  ON companies (lifecycle_state, purge_at)
  WHERE lifecycle_state = 'ARCHIVED';

-- Every legacy creator needs a permanent company.  Reuse an existing active
-- migrated-data company when present, otherwise create exactly one per workspace.
INSERT INTO companies (organization_id, name)
SELECT orphan.organization_id, 'Перенесённые данные'
FROM (
  SELECT DISTINCT organization_id
  FROM creators
  WHERE company_id IS NULL
) AS orphan
WHERE NOT EXISTS (
  SELECT 1
  FROM companies existing
  WHERE existing.organization_id = orphan.organization_id
    AND lower(existing.name) = lower('Перенесённые данные')
    AND existing.archived_at IS NULL
);

UPDATE creators AS creator
SET company_id = (
  SELECT company.id
  FROM companies company
  WHERE company.organization_id = creator.organization_id
    AND lower(company.name) = lower('Перенесённые данные')
    AND company.archived_at IS NULL
  ORDER BY company.created_at, company.id
  LIMIT 1
)
WHERE creator.company_id IS NULL;

ALTER TABLE creators
  ADD COLUMN login_user_id uuid,
  ADD CONSTRAINT creators_id_organization_key UNIQUE (id, organization_id);

ALTER TABLE creators
  DROP CONSTRAINT creators_company_id_fkey,
  ADD CONSTRAINT creators_company_workspace_fkey
    FOREIGN KEY (company_id, organization_id)
    REFERENCES companies (id, organization_id) ON DELETE CASCADE,
  ADD CONSTRAINT creators_login_membership_fkey
    FOREIGN KEY (organization_id, login_user_id)
    REFERENCES organization_memberships (organization_id, user_id)
    ON DELETE SET NULL (login_user_id),
  ALTER COLUMN company_id SET NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION validate_creator_login_membership() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.login_user_id IS NOT NULL AND NOT EXISTS (
    SELECT 1
    FROM organization_memberships membership
    WHERE membership.organization_id = NEW.organization_id
      AND membership.user_id = NEW.login_user_id
      AND membership.membership_role = 'CREATOR'
  ) THEN
    RAISE EXCEPTION 'creator login user must have a CREATOR membership in the same workspace'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER creators_validate_login_membership
  BEFORE INSERT OR UPDATE OF organization_id, login_user_id ON creators
  FOR EACH ROW EXECUTE FUNCTION validate_creator_login_membership();

-- +goose StatementBegin
CREATE FUNCTION protect_creator_membership_role() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.membership_role = 'CREATOR'
     AND NEW.membership_role <> 'CREATOR'
     AND EXISTS (
       SELECT 1 FROM creators creator
       WHERE creator.organization_id = OLD.organization_id
         AND creator.login_user_id = OLD.user_id
     ) THEN
    RAISE EXCEPTION 'creator membership still owns creator profiles'
      USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER organization_memberships_protect_creator_role
  BEFORE UPDATE OF membership_role ON organization_memberships
  FOR EACH ROW EXECUTE FUNCTION protect_creator_membership_role();

-- +goose StatementBegin
CREATE FUNCTION prevent_creator_company_move() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.company_id IS DISTINCT FROM OLD.company_id THEN
    RAISE EXCEPTION 'creator profiles cannot be moved between companies'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER creators_company_immutable
  BEFORE UPDATE OF company_id ON creators
  FOR EACH ROW EXECUTE FUNCTION prevent_creator_company_move();

CREATE INDEX creators_organization_company_idx
  ON creators (organization_id, company_id);
CREATE INDEX creators_company_workspace_idx
  ON creators (company_id, organization_id);
CREATE INDEX creators_login_workspace_idx
  ON creators (organization_id, login_user_id)
  WHERE login_user_id IS NOT NULL;
CREATE INDEX creators_login_user_idx
  ON creators (login_user_id)
  WHERE login_user_id IS NOT NULL;
CREATE UNIQUE INDEX creators_one_profile_per_user_company_idx
  ON creators (company_id, login_user_id)
  WHERE login_user_id IS NOT NULL;

ALTER TABLE sessions
  ADD COLUMN active_company_id uuid REFERENCES companies(id) ON DELETE SET NULL,
  ADD COLUMN active_creator_id uuid REFERENCES creators(id) ON DELETE SET NULL;

CREATE INDEX sessions_active_company_idx
  ON sessions (active_company_id)
  WHERE active_company_id IS NOT NULL;
CREATE INDEX sessions_active_creator_idx
  ON sessions (active_creator_id)
  WHERE active_creator_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION validate_session_context() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  workspace_id uuid;
  workspace_role membership_role;
  creator_company_id uuid;
BEGIN
  IF NEW.revoked_at IS NOT NULL THEN
    RETURN NEW;
  END IF;

  SELECT membership.organization_id, membership.membership_role
  INTO workspace_id, workspace_role
  FROM organization_memberships membership
  WHERE membership.user_id = NEW.user_id
  FOR KEY SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'session user has no workspace membership'
      USING ERRCODE = '23503';
  END IF;

  IF workspace_role = 'OWNER' THEN
    IF NEW.active_creator_id IS NOT NULL THEN
      RAISE EXCEPTION 'owner sessions cannot select a creator profile'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.active_company_id IS NOT NULL THEN
      PERFORM 1 FROM companies company
      WHERE company.id = NEW.active_company_id
        AND company.organization_id = workspace_id
      FOR KEY SHARE;
      IF NOT FOUND THEN
        RAISE EXCEPTION 'owner active company belongs to another workspace'
          USING ERRCODE = '23514';
      END IF;
    END IF;
  ELSIF workspace_role = 'MANAGER' THEN
    IF NEW.active_creator_id IS NOT NULL THEN
      RAISE EXCEPTION 'manager sessions cannot select a creator profile'
        USING ERRCODE = '23514';
    END IF;
    -- A context-free session is the expand-compatible login state. The API
    -- selects an assigned company in the subsequent context step.
    IF NEW.active_company_id IS NOT NULL THEN
      PERFORM 1 FROM manager_company_assignments assignment
      WHERE assignment.organization_id = workspace_id
        AND assignment.manager_user_id = NEW.user_id
        AND assignment.company_id = NEW.active_company_id
      FOR KEY SHARE;
      IF NOT FOUND THEN
        RAISE EXCEPTION 'manager active company is not assigned'
          USING ERRCODE = '23514';
      END IF;
    END IF;
  ELSIF workspace_role = 'CREATOR' THEN
    IF NEW.active_creator_id IS NULL THEN
      IF NEW.active_company_id IS NOT NULL THEN
        RAISE EXCEPTION 'creator active company requires an active creator profile'
          USING ERRCODE = '23514';
      END IF;
      -- Preserve the legacy login INSERT; the profile context is selected by
      -- the API after authentication.
      RETURN NEW;
    END IF;
    SELECT creator.company_id INTO creator_company_id
    FROM creators creator
    WHERE creator.id = NEW.active_creator_id
      AND creator.organization_id = workspace_id
      AND creator.login_user_id = NEW.user_id
    FOR KEY SHARE;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'active creator profile does not belong to the session user'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.active_company_id IS NULL THEN
      NEW.active_company_id := creator_company_id;
    ELSIF NEW.active_company_id <> creator_company_id THEN
      RAISE EXCEPTION 'creator active company does not match the active profile'
        USING ERRCODE = '23514';
    END IF;
  END IF;

  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sessions_validate_context
  BEFORE INSERT OR UPDATE OF user_id, active_company_id, active_creator_id, revoked_at ON sessions
  FOR EACH ROW EXECUTE FUNCTION validate_session_context();

CREATE TABLE manager_company_assignments (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL,
  manager_user_id uuid NOT NULL,
  company_id uuid NOT NULL,
  membership_role membership_role
    GENERATED ALWAYS AS ('MANAGER'::membership_role) STORED,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT manager_company_assignments_membership_fkey
    FOREIGN KEY (organization_id, manager_user_id, membership_role)
    REFERENCES organization_memberships (organization_id, user_id, membership_role)
    ON DELETE CASCADE,
  CONSTRAINT manager_company_assignments_company_fkey
    FOREIGN KEY (company_id, organization_id)
    REFERENCES companies (id, organization_id) ON DELETE CASCADE,
  CONSTRAINT manager_company_assignments_manager_company_key
    UNIQUE (manager_user_id, company_id)
);

CREATE INDEX manager_company_assignments_workspace_manager_idx
  ON manager_company_assignments (organization_id, manager_user_id, membership_role);
CREATE INDEX manager_company_assignments_company_idx
  ON manager_company_assignments (company_id, organization_id);

CREATE TABLE manager_company_permissions (
  assignment_id uuid NOT NULL REFERENCES manager_company_assignments(id) ON DELETE CASCADE,
  permission manager_permission NOT NULL,
  granted_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (assignment_id, permission)
);

-- Legacy managers see every current company. Company/card viewing is implicit in
-- the assignment itself; permissions grant only the listed actions.
INSERT INTO manager_company_assignments (organization_id, manager_user_id, company_id)
SELECT membership.organization_id, membership.user_id, company.id
FROM organization_memberships membership
JOIN companies company
  ON company.organization_id = membership.organization_id
WHERE membership.membership_role = 'MANAGER'
  AND company.archived_at IS NULL;

INSERT INTO manager_company_permissions (assignment_id, permission)
SELECT assignment.id, permission.permission
FROM manager_company_assignments assignment
JOIN organization_memberships membership
  ON membership.organization_id = assignment.organization_id
 AND membership.user_id = assignment.manager_user_id
CROSS JOIN LATERAL unnest(
  CASE membership.role
    WHEN 'ANALYST' THEN ARRAY[
      'STATS_VIEW',
      'STATS_EXPORT',
      'CREATOR_CREATE',
      'CREATOR_EDIT',
      'CREATOR_ARCHIVE',
      'CREATOR_DELETE',
      'CREATOR_ACCOUNT_MANAGE',
      'SOCIAL_CONNECT',
      'SYNC_MANAGE',
      'CREDENTIAL_EDIT'
    ]::manager_permission[]
    WHEN 'VIEWER' THEN ARRAY[
      'STATS_VIEW',
      'STATS_EXPORT'
    ]::manager_permission[]
    ELSE ARRAY[]::manager_permission[]
  END
) AS permission(permission);

-- +goose StatementBegin
CREATE FUNCTION invalidate_deleted_session_context() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_TABLE_NAME = 'manager_company_assignments' THEN
    UPDATE sessions
    SET revoked_at = COALESCE(revoked_at, now()),
        active_company_id = NULL,
        active_creator_id = NULL
    WHERE user_id = OLD.manager_user_id
      AND active_company_id = OLD.company_id
      AND revoked_at IS NULL;
  ELSIF TG_TABLE_NAME = 'creators' THEN
    UPDATE sessions
    SET revoked_at = COALESCE(revoked_at, now()),
        active_company_id = NULL,
        active_creator_id = NULL
    WHERE active_creator_id = OLD.id
      AND revoked_at IS NULL;
  ELSIF TG_TABLE_NAME = 'companies' THEN
    UPDATE sessions
    SET revoked_at = COALESCE(revoked_at, now()),
        active_company_id = NULL,
        active_creator_id = NULL
    WHERE active_company_id = OLD.id
      AND revoked_at IS NULL;
  END IF;
  IF TG_OP = 'UPDATE' THEN
    RETURN NEW;
  END IF;
  RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER manager_company_assignments_invalidate_sessions
  BEFORE DELETE OR UPDATE OF company_id ON manager_company_assignments
  FOR EACH ROW EXECUTE FUNCTION invalidate_deleted_session_context();
CREATE TRIGGER creators_invalidate_sessions
  BEFORE DELETE ON creators
  FOR EACH ROW EXECUTE FUNCTION invalidate_deleted_session_context();
CREATE TRIGGER companies_invalidate_sessions
  BEFORE DELETE ON companies
  FOR EACH ROW EXECUTE FUNCTION invalidate_deleted_session_context();

-- Workspace and manager identity define the tenant edge and cannot be moved.
-- Company reassignment remains supported and is handled by the invalidation
-- trigger above.
-- +goose StatementBegin
CREATE FUNCTION prevent_manager_assignment_identity_move() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.organization_id IS DISTINCT FROM OLD.organization_id
     OR NEW.manager_user_id IS DISTINCT FROM OLD.manager_user_id THEN
    RAISE EXCEPTION 'manager assignment workspace and manager are immutable'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER manager_company_assignments_identity_immutable
  BEFORE UPDATE OF organization_id, manager_user_id ON manager_company_assignments
  FOR EACH ROW EXECUTE FUNCTION prevent_manager_assignment_identity_move();

-- +goose StatementBegin
CREATE FUNCTION invalidate_changed_membership_sessions() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.membership_role IS DISTINCT FROM OLD.membership_role THEN
    UPDATE sessions
    SET revoked_at = COALESCE(revoked_at, now()),
        active_company_id = NULL,
        active_creator_id = NULL
    WHERE user_id = NEW.user_id
      AND revoked_at IS NULL;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER organization_memberships_invalidate_changed_sessions
  AFTER UPDATE OF role, membership_role ON organization_memberships
  FOR EACH ROW EXECUTE FUNCTION invalidate_changed_membership_sessions();

ALTER TABLE platform_accounts
  ADD COLUMN company_id uuid,
  ADD CONSTRAINT platform_accounts_id_organization_key UNIQUE (id, organization_id);

-- Prefer the explicit company VK owner, then the active creator assignment.
UPDATE platform_accounts AS account
SET company_id = scoped.company_id
FROM (
  SELECT DISTINCT ON (candidate.account_id) candidate.account_id, candidate.company_id
  FROM (
  SELECT vk.platform_account_id AS account_id, vk.company_id, 0 AS priority
  FROM company_vk_accounts vk
  WHERE vk.platform_account_id IS NOT NULL
  UNION ALL
  SELECT assignment.platform_account_id, creator.company_id, 1 AS priority
  FROM creator_account_assignments assignment
  JOIN creators creator ON creator.id = assignment.creator_id
  WHERE assignment.valid_to IS NULL
  UNION ALL
  SELECT publication.platform_account_id, creator.company_id, 2 AS priority
  FROM publications publication
  JOIN creators creator ON creator.id = publication.creator_id
  ) AS candidate
  ORDER BY candidate.account_id, candidate.priority, candidate.company_id
) AS scoped
WHERE account.id = scoped.account_id
  AND account.organization_id = (
    SELECT company.organization_id FROM companies company WHERE company.id = scoped.company_id
  )
  AND account.company_id IS NULL;

-- Unassigned legacy accounts still need a deterministic company scope.  This
-- also makes sync-target backfill total without guessing another company.
INSERT INTO companies (organization_id, name)
SELECT orphan.organization_id, 'Перенесённые данные'
FROM (
  SELECT DISTINCT organization_id
  FROM platform_accounts
  WHERE company_id IS NULL
) AS orphan
WHERE NOT EXISTS (
  SELECT 1
  FROM companies existing
  WHERE existing.organization_id = orphan.organization_id
    AND lower(existing.name) = lower('Перенесённые данные')
    AND existing.archived_at IS NULL
);

UPDATE platform_accounts AS account
SET company_id = (
  SELECT company.id
  FROM companies company
  WHERE company.organization_id = account.organization_id
    AND lower(company.name) = lower('Перенесённые данные')
    AND company.archived_at IS NULL
  ORDER BY company.created_at, company.id
  LIMIT 1
)
WHERE account.company_id IS NULL;

ALTER TABLE platform_accounts
  ADD CONSTRAINT platform_accounts_company_workspace_fkey
    FOREIGN KEY (company_id, organization_id)
    REFERENCES companies (id, organization_id) ON DELETE CASCADE;

-- Legacy application writes do not yet pass company_id. Serialize fallback
-- company creation per workspace so those writes remain valid during expand.
-- +goose StatementBegin
CREATE FUNCTION scope_platform_account() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  fallback_company_id uuid;
BEGIN
  IF TG_OP = 'UPDATE'
     AND NEW.organization_id IS DISTINCT FROM OLD.organization_id THEN
    RAISE EXCEPTION 'platform accounts cannot move between workspaces'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.company_id IS NULL THEN
    PERFORM 1 FROM organizations workspace
    WHERE workspace.id = NEW.organization_id
    FOR UPDATE;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'platform account workspace does not exist'
        USING ERRCODE = '23503';
    END IF;

    SELECT company.id INTO fallback_company_id
    FROM companies company
    WHERE company.organization_id = NEW.organization_id
      AND lower(company.name) = lower('Перенесённые данные')
      AND company.archived_at IS NULL
    ORDER BY company.created_at, company.id
    LIMIT 1;

    IF fallback_company_id IS NULL THEN
      INSERT INTO companies (organization_id, name)
      VALUES (NEW.organization_id, 'Перенесённые данные')
      RETURNING id INTO fallback_company_id;
    END IF;
    NEW.company_id := fallback_company_id;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM companies company
    WHERE company.id = NEW.company_id
      AND company.organization_id = NEW.organization_id
  ) THEN
    RAISE EXCEPTION 'platform account company does not belong to its workspace'
      USING ERRCODE = '23514';
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.company_id IS DISTINCT FROM OLD.company_id THEN
    IF NOT EXISTS (
      SELECT 1 FROM companies company
      WHERE company.id = OLD.company_id
        AND company.organization_id = OLD.organization_id
        AND lower(company.name) = lower('Перенесённые данные')
        AND company.archived_at IS NULL
    ) OR EXISTS (
      SELECT 1 FROM creator_account_assignments assignment
      WHERE assignment.platform_account_id = OLD.id
        AND assignment.valid_to IS NULL
    ) OR EXISTS (
      SELECT 1 FROM company_vk_accounts vk
      WHERE vk.platform_account_id = OLD.id
    ) OR EXISTS (
      SELECT 1 FROM publications publication
      WHERE publication.platform_account_id = OLD.id
    ) THEN
      RAISE EXCEPTION 'owned platform accounts cannot be moved between companies'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER platform_accounts_scope
  BEFORE INSERT OR UPDATE OF organization_id, company_id ON platform_accounts
  FOR EACH ROW EXECUTE FUNCTION scope_platform_account();

-- +goose StatementBegin
CREATE FUNCTION cascade_platform_account_company_to_sync_targets() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE sync_targets
  SET company_id = NEW.company_id
  WHERE target_type = 'PLATFORM_ACCOUNT'
    AND target_id = NEW.id
    AND organization_id = OLD.organization_id
    AND company_id IS DISTINCT FROM NEW.company_id;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER platform_accounts_cascade_sync_scope
  AFTER UPDATE OF company_id ON platform_accounts
  FOR EACH ROW
  WHEN (OLD.company_id IS DISTINCT FROM NEW.company_id)
  EXECUTE FUNCTION cascade_platform_account_company_to_sync_targets();

ALTER TABLE platform_accounts ALTER COLUMN company_id SET NOT NULL;

CREATE INDEX platform_accounts_company_workspace_idx
  ON platform_accounts (company_id, organization_id);

-- Explicit company VK ownership wins over an incompatible active legacy
-- creator assignment. Preserve the association as history by closing it.
UPDATE creator_account_assignments AS assignment
SET valid_to = GREATEST(now(), assignment.valid_from + interval '1 microsecond')
FROM creators creator, platform_accounts account, company_vk_accounts vk
WHERE creator.id = assignment.creator_id
  AND account.id = assignment.platform_account_id
  AND vk.platform_account_id = account.id
  AND creator.organization_id = account.organization_id
  AND vk.organization_id = account.organization_id
  AND creator.company_id <> account.company_id
  AND assignment.valid_to IS NULL;

ALTER TABLE creator_account_assignments ADD COLUMN organization_id uuid;

UPDATE creator_account_assignments AS assignment
SET organization_id = creator.organization_id
FROM creators creator
JOIN platform_accounts account
  ON account.organization_id = creator.organization_id
WHERE creator.id = assignment.creator_id
  AND account.id = assignment.platform_account_id;

ALTER TABLE creator_account_assignments
  ALTER COLUMN organization_id SET NOT NULL,
  DROP CONSTRAINT creator_account_assignments_creator_id_fkey,
  ADD CONSTRAINT creator_account_assignments_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id)
    REFERENCES creators (id, organization_id) ON DELETE CASCADE,
  DROP CONSTRAINT creator_account_assignments_platform_account_id_fkey,
  ADD CONSTRAINT creator_account_assignments_account_workspace_fkey
    FOREIGN KEY (platform_account_id, organization_id)
    REFERENCES platform_accounts (id, organization_id) ON DELETE CASCADE;

-- +goose StatementBegin
CREATE FUNCTION scope_creator_account_assignment() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  creator_workspace uuid;
  creator_company uuid;
  account_workspace uuid;
  account_company uuid;
BEGIN
  SELECT creator.organization_id, creator.company_id,
         account.organization_id, account.company_id
  INTO creator_workspace, creator_company, account_workspace, account_company
  FROM creators creator
  CROSS JOIN platform_accounts account
  WHERE creator.id = NEW.creator_id
    AND account.id = NEW.platform_account_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'creator or platform account does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF creator_workspace <> account_workspace THEN
    RAISE EXCEPTION 'creator and platform account belong to different workspaces'
      USING ERRCODE = '23514';
  END IF;
  IF account_company IS NOT NULL
     AND account_company <> creator_company
     AND NEW.valid_to IS NULL THEN
    IF EXISTS (
      SELECT 1 FROM companies company
      WHERE company.id = account_company
        AND company.organization_id = account_workspace
        AND lower(company.name) = lower('Перенесённые данные')
        AND company.archived_at IS NULL
    ) AND NOT EXISTS (
      SELECT 1 FROM creator_account_assignments existing
      WHERE existing.platform_account_id = NEW.platform_account_id
        AND existing.id IS DISTINCT FROM NEW.id
        AND existing.valid_to IS NULL
    ) AND NOT EXISTS (
      SELECT 1 FROM company_vk_accounts vk
      WHERE vk.platform_account_id = NEW.platform_account_id
    ) THEN
      UPDATE platform_accounts
      SET company_id = creator_company, updated_at = now()
      WHERE id = NEW.platform_account_id
        AND organization_id = creator_workspace;
      account_company := creator_company;
    ELSE
      RAISE EXCEPTION 'creator and platform account belong to different companies'
        USING ERRCODE = '23514';
    END IF;
  END IF;

  NEW.organization_id := creator_workspace;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER creator_account_assignments_scope
  BEFORE INSERT OR UPDATE OF creator_id, platform_account_id, organization_id, valid_to
  ON creator_account_assignments
  FOR EACH ROW EXECUTE FUNCTION scope_creator_account_assignment();

UPDATE creator_account_assignments SET creator_id = creator_id;

CREATE INDEX creator_account_assignments_account_workspace_idx
  ON creator_account_assignments (platform_account_id, organization_id);

ALTER TABLE sync_targets ADD COLUMN company_id uuid;

UPDATE sync_targets AS target
SET company_id = account.company_id
FROM platform_accounts account
WHERE target.target_type = 'PLATFORM_ACCOUNT'
  AND target.target_id = account.id
  AND target.organization_id = account.organization_id;

UPDATE sync_targets AS target
SET company_id = creator.company_id
FROM creators creator
WHERE target.target_type = 'CREATOR'
  AND target.target_id = creator.id
  AND target.organization_id = creator.organization_id
  AND target.company_id IS NULL;

INSERT INTO companies (organization_id, name)
SELECT orphan.organization_id, 'Перенесённые данные'
FROM (
  SELECT DISTINCT organization_id
  FROM sync_targets
  WHERE company_id IS NULL
) AS orphan
WHERE NOT EXISTS (
  SELECT 1
  FROM companies existing
  WHERE existing.organization_id = orphan.organization_id
    AND lower(existing.name) = lower('Перенесённые данные')
    AND existing.archived_at IS NULL
);

UPDATE sync_targets AS target
SET company_id = (
  SELECT company.id
  FROM companies company
  WHERE company.organization_id = target.organization_id
    AND lower(company.name) = lower('Перенесённые данные')
    AND company.archived_at IS NULL
  ORDER BY company.created_at, company.id
  LIMIT 1
)
WHERE target.company_id IS NULL;

ALTER TABLE sync_targets
  ADD CONSTRAINT sync_targets_company_workspace_fkey
    FOREIGN KEY (company_id, organization_id)
    REFERENCES companies (id, organization_id) ON DELETE CASCADE;

-- sync_targets are reconstructible operational jobs, not business history.
-- Remove v19 rows whose polymorphic resource is missing, unsupported, or in a
-- different workspace; their run telemetry cannot remain without the job.
DELETE FROM sync_runs run
USING sync_targets target
WHERE run.target_id = target.id
  AND (
    target.target_type NOT IN ('PLATFORM_ACCOUNT', 'CREATOR')
    OR target.target_type = 'PLATFORM_ACCOUNT' AND NOT EXISTS (
      SELECT 1 FROM platform_accounts account
      WHERE account.id = target.target_id
        AND account.organization_id = target.organization_id
    )
    OR target.target_type = 'CREATOR' AND NOT EXISTS (
      SELECT 1 FROM creators creator
      WHERE creator.id = target.target_id
        AND creator.organization_id = target.organization_id
    )
  );

DELETE FROM sync_targets target
WHERE target.target_type NOT IN ('PLATFORM_ACCOUNT', 'CREATOR')
   OR target.target_type = 'PLATFORM_ACCOUNT' AND NOT EXISTS (
     SELECT 1 FROM platform_accounts account
     WHERE account.id = target.target_id
       AND account.organization_id = target.organization_id
   )
   OR target.target_type = 'CREATOR' AND NOT EXISTS (
     SELECT 1 FROM creators creator
     WHERE creator.id = target.target_id
       AND creator.organization_id = target.organization_id
   );

-- +goose StatementBegin
CREATE FUNCTION scope_sync_target() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  target_workspace uuid;
  target_company uuid;
BEGIN
  IF NEW.target_type = 'PLATFORM_ACCOUNT' THEN
    SELECT account.organization_id, account.company_id
    INTO target_workspace, target_company
    FROM platform_accounts account
    WHERE account.id = NEW.target_id
    FOR KEY SHARE;
  ELSIF NEW.target_type = 'CREATOR' THEN
    SELECT creator.organization_id, creator.company_id
    INTO target_workspace, target_company
    FROM creators creator
    WHERE creator.id = NEW.target_id
    FOR KEY SHARE;
  ELSE
    RAISE EXCEPTION 'unsupported sync target type %', NEW.target_type
      USING ERRCODE = '23514';
  END IF;

  IF target_workspace IS NULL THEN
    RAISE EXCEPTION 'sync target resource does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF NEW.organization_id <> target_workspace THEN
    RAISE EXCEPTION 'sync target resource belongs to another workspace'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.company_id IS NULL THEN
    NEW.company_id := target_company;
  ELSIF NEW.company_id <> target_company THEN
    RAISE EXCEPTION 'sync target resource belongs to another company'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sync_targets_scope
  BEFORE INSERT OR UPDATE OF target_type, target_id, organization_id, company_id
  ON sync_targets
  FOR EACH ROW EXECUTE FUNCTION scope_sync_target();

-- Validate already-backfilled polymorphic links before making scope mandatory.
UPDATE sync_targets SET target_id = target_id;
ALTER TABLE sync_targets ALTER COLUMN company_id SET NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION delete_polymorphic_sync_targets() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_TABLE_NAME = 'platform_accounts' THEN
    DELETE FROM sync_targets
    WHERE target_type = 'PLATFORM_ACCOUNT'
      AND target_id = OLD.id;
  ELSIF TG_TABLE_NAME = 'creators' THEN
    DELETE FROM sync_targets
    WHERE target_type = 'CREATOR'
      AND target_id = OLD.id;
  END IF;
  RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER platform_accounts_delete_sync_targets
  AFTER DELETE ON platform_accounts
  FOR EACH ROW EXECUTE FUNCTION delete_polymorphic_sync_targets();
CREATE TRIGGER creators_delete_sync_targets
  AFTER DELETE ON creators
  FOR EACH ROW EXECUTE FUNCTION delete_polymorphic_sync_targets();

CREATE INDEX sync_targets_company_workspace_idx
  ON sync_targets (company_id, organization_id);

-- Paired resources carry the workspace in their foreign key. Additional
-- triggers cover company equality where the child table has no company_id.
-- +goose StatementBegin
CREATE FUNCTION validate_publication_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  creator_workspace uuid;
  creator_company uuid;
  account_workspace uuid;
  account_company uuid;
BEGIN
  IF TG_OP = 'UPDATE' AND (
    NEW.organization_id IS DISTINCT FROM OLD.organization_id
    OR NEW.creator_id IS DISTINCT FROM OLD.creator_id
    OR NEW.platform_account_id IS DISTINCT FROM OLD.platform_account_id
  ) THEN
    RAISE EXCEPTION 'publication ownership cannot be changed'
      USING ERRCODE = '23514';
  END IF;
  SELECT creator.organization_id, creator.company_id,
         account.organization_id, account.company_id
  INTO creator_workspace, creator_company, account_workspace, account_company
  FROM creators creator
  CROSS JOIN platform_accounts account
  WHERE creator.id = NEW.creator_id
    AND account.id = NEW.platform_account_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'publication creator or platform account does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF NEW.organization_id <> creator_workspace
     OR NEW.organization_id <> account_workspace THEN
    RAISE EXCEPTION 'publication resources belong to another workspace'
      USING ERRCODE = '23514';
  END IF;
  IF creator_company <> account_company THEN
    RAISE EXCEPTION 'publication resources belong to different companies'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER publications_validate_scope
  BEFORE INSERT OR UPDATE OF organization_id, creator_id, platform_account_id
  ON publications
  FOR EACH ROW EXECUTE FUNCTION validate_publication_scope();

UPDATE publications SET creator_id = creator_id;
ALTER TABLE publications
  DROP CONSTRAINT publications_creator_id_fkey,
  ADD CONSTRAINT publications_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id)
    REFERENCES creators (id, organization_id) ON DELETE CASCADE,
  DROP CONSTRAINT publications_platform_account_id_fkey,
  ADD CONSTRAINT publications_account_workspace_fkey
    FOREIGN KEY (platform_account_id, organization_id)
    REFERENCES platform_accounts (id, organization_id) ON DELETE CASCADE;

ALTER TABLE company_vk_accounts
  ADD CONSTRAINT company_vk_accounts_id_organization_key UNIQUE (id, organization_id);

-- +goose StatementBegin
CREATE FUNCTION validate_company_vk_account_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  account_workspace uuid;
  account_company uuid;
BEGIN
  IF TG_OP = 'UPDATE' AND (
    NEW.organization_id IS DISTINCT FROM OLD.organization_id
    OR NEW.company_id IS DISTINCT FROM OLD.company_id
  ) THEN
    RAISE EXCEPTION 'company VK accounts cannot move between workspaces or companies'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.platform_account_id IS NULL THEN
    RETURN NEW;
  END IF;

  SELECT account.organization_id, account.company_id
  INTO account_workspace, account_company
  FROM platform_accounts account
  WHERE account.id = NEW.platform_account_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'company VK platform account does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF account_workspace <> NEW.organization_id THEN
    RAISE EXCEPTION 'company VK platform account belongs to another workspace'
      USING ERRCODE = '23514';
  END IF;
  IF account_company <> NEW.company_id THEN
    IF EXISTS (
      SELECT 1 FROM companies company
      WHERE company.id = account_company
        AND company.organization_id = account_workspace
        AND lower(company.name) = lower('Перенесённые данные')
        AND company.archived_at IS NULL
    ) AND NOT EXISTS (
      SELECT 1 FROM creator_account_assignments assignment
      WHERE assignment.platform_account_id = NEW.platform_account_id
        AND assignment.valid_to IS NULL
    ) AND NOT EXISTS (
      SELECT 1 FROM company_vk_accounts existing
      WHERE existing.platform_account_id = NEW.platform_account_id
        AND existing.id IS DISTINCT FROM NEW.id
    ) THEN
      UPDATE platform_accounts
      SET company_id = NEW.company_id, updated_at = now()
      WHERE id = NEW.platform_account_id
        AND organization_id = NEW.organization_id;
      account_company := NEW.company_id;
    ELSE
      RAISE EXCEPTION 'company VK platform account belongs to another company'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER company_vk_accounts_validate_scope
  BEFORE INSERT OR UPDATE OF organization_id, company_id, platform_account_id
  ON company_vk_accounts
  FOR EACH ROW EXECUTE FUNCTION validate_company_vk_account_scope();

UPDATE company_vk_accounts
SET platform_account_id = platform_account_id
WHERE platform_account_id IS NOT NULL;

ALTER TABLE company_vk_accounts
  DROP CONSTRAINT company_vk_accounts_company_id_fkey,
  ADD CONSTRAINT company_vk_accounts_company_workspace_fkey
    FOREIGN KEY (company_id, organization_id)
    REFERENCES companies (id, organization_id) ON DELETE CASCADE,
  DROP CONSTRAINT company_vk_accounts_platform_account_id_fkey,
  ADD CONSTRAINT company_vk_accounts_platform_account_workspace_fkey
    FOREIGN KEY (platform_account_id, organization_id)
    REFERENCES platform_accounts (id, organization_id)
    ON DELETE SET NULL (platform_account_id);

ALTER TABLE oauth_connections
  DROP CONSTRAINT oauth_connections_platform_account_id_fkey,
  ADD CONSTRAINT oauth_connections_account_workspace_fkey
    FOREIGN KEY (platform_account_id, organization_id)
    REFERENCES platform_accounts (id, organization_id) ON DELETE CASCADE;

ALTER TABLE oauth_states
  DROP CONSTRAINT oauth_states_creator_id_fkey,
  ADD CONSTRAINT oauth_states_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id)
    REFERENCES creators (id, organization_id) ON DELETE CASCADE,
  DROP CONSTRAINT oauth_states_company_vk_account_id_fkey,
  ADD CONSTRAINT oauth_states_company_vk_workspace_fkey
    FOREIGN KEY (company_vk_account_id, organization_id)
    REFERENCES company_vk_accounts (id, organization_id) ON DELETE CASCADE;

ALTER TABLE oauth_account_selections
  DROP CONSTRAINT oauth_account_selections_creator_id_fkey,
  ADD CONSTRAINT oauth_account_selections_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id)
    REFERENCES creators (id, organization_id) ON DELETE CASCADE;

ALTER TABLE creator_history_events
  DROP CONSTRAINT creator_history_events_creator_id_fkey,
  ADD CONSTRAINT creator_history_events_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id)
    REFERENCES creators (id, organization_id) ON DELETE CASCADE;

ALTER TABLE creator_vk_assignments ADD COLUMN organization_id uuid;

UPDATE creator_vk_assignments assignment
SET organization_id = creator.organization_id
FROM creators creator
JOIN company_vk_accounts vk
  ON vk.organization_id = creator.organization_id
 AND vk.company_id = creator.company_id
WHERE creator.id = assignment.creator_id
  AND vk.id = assignment.company_vk_account_id;

ALTER TABLE creator_vk_assignments
  ALTER COLUMN organization_id SET NOT NULL,
  DROP CONSTRAINT creator_vk_assignments_creator_id_fkey,
  ADD CONSTRAINT creator_vk_assignments_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id)
    REFERENCES creators (id, organization_id) ON DELETE CASCADE,
  DROP CONSTRAINT creator_vk_assignments_company_vk_account_id_fkey,
  ADD CONSTRAINT creator_vk_assignments_account_workspace_fkey
    FOREIGN KEY (company_vk_account_id, organization_id)
    REFERENCES company_vk_accounts (id, organization_id) ON DELETE CASCADE;

-- +goose StatementBegin
CREATE FUNCTION validate_creator_vk_assignment_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  creator_workspace uuid;
  creator_company uuid;
  account_workspace uuid;
  account_company uuid;
BEGIN
  SELECT creator.organization_id, creator.company_id,
         account.organization_id, account.company_id
  INTO creator_workspace, creator_company, account_workspace, account_company
  FROM creators creator
  CROSS JOIN company_vk_accounts account
  WHERE creator.id = NEW.creator_id
    AND account.id = NEW.company_vk_account_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'creator or company VK account does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF creator_workspace <> account_workspace OR creator_company <> account_company THEN
    RAISE EXCEPTION 'creator and company VK account must share workspace and company'
      USING ERRCODE = '23514';
  END IF;
  NEW.organization_id := creator_workspace;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER creator_vk_assignments_validate_scope
  BEFORE INSERT OR UPDATE OF creator_id, company_vk_account_id, organization_id
  ON creator_vk_assignments
  FOR EACH ROW EXECUTE FUNCTION validate_creator_vk_assignment_scope();
UPDATE creator_vk_assignments SET creator_id = creator_id;

-- +goose StatementBegin
CREATE FUNCTION prevent_content_group_creator_move() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.creator_id IS DISTINCT FROM OLD.creator_id THEN
    RAISE EXCEPTION 'content groups cannot move between creators'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER content_groups_creator_immutable
  BEFORE UPDATE OF creator_id ON content_groups
  FOR EACH ROW EXECUTE FUNCTION prevent_content_group_creator_move();

-- +goose StatementBegin
CREATE FUNCTION validate_content_group_member_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  group_creator uuid;
  publication_creator uuid;
BEGIN
  SELECT content_group.creator_id, publication.creator_id
  INTO group_creator, publication_creator
  FROM content_groups content_group
  CROSS JOIN publications publication
  WHERE content_group.id = NEW.content_group_id
    AND publication.id = NEW.publication_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'content group or publication does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF group_creator <> publication_creator THEN
    RAISE EXCEPTION 'content group publication belongs to another creator'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER content_group_members_validate_scope
  BEFORE INSERT OR UPDATE OF content_group_id, publication_id
  ON content_group_members
  FOR EACH ROW EXECUTE FUNCTION validate_content_group_member_scope();
UPDATE content_group_members SET publication_id = publication_id;

-- +goose StatementBegin
CREATE FUNCTION validate_content_match_suggestion_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  publication_a_creator uuid;
  publication_b_creator uuid;
BEGIN
  SELECT publication_a.creator_id, publication_b.creator_id
  INTO publication_a_creator, publication_b_creator
  FROM publications publication_a
  CROSS JOIN publications publication_b
  WHERE publication_a.id = NEW.publication_a_id
    AND publication_b.id = NEW.publication_b_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'content match publication does not exist'
      USING ERRCODE = '23503';
  END IF;
  IF NEW.creator_id <> publication_a_creator
     OR NEW.creator_id <> publication_b_creator THEN
    RAISE EXCEPTION 'content match publications belong to another creator'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER content_match_suggestions_validate_scope
  BEFORE INSERT OR UPDATE OF creator_id, publication_a_id, publication_b_id
  ON content_match_suggestions
  FOR EACH ROW EXECUTE FUNCTION validate_content_match_suggestion_scope();
UPDATE content_match_suggestions SET creator_id = creator_id;

-- +goose StatementBegin
CREATE FUNCTION validate_platform_deletion_request_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.organization_id IS NOT NULL AND NEW.platform_account_id IS NOT NULL
     AND NOT EXISTS (
       SELECT 1 FROM platform_accounts account
       WHERE account.id = NEW.platform_account_id
         AND account.organization_id = NEW.organization_id
     ) THEN
    RAISE EXCEPTION 'platform deletion request account belongs to another workspace'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER platform_data_deletion_requests_validate_scope
  BEFORE INSERT OR UPDATE OF organization_id, platform_account_id
  ON platform_data_deletion_requests
  FOR EACH ROW EXECUTE FUNCTION validate_platform_deletion_request_scope();
UPDATE platform_data_deletion_requests SET platform_account_id = platform_account_id;

-- Tenant-owned rows cascade with their workspace. Historical actor references
-- survive user deletion with a NULL actor.
ALTER TABLE creators
  DROP CONSTRAINT creators_organization_id_fkey,
  ADD CONSTRAINT creators_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE platform_accounts
  DROP CONSTRAINT platform_accounts_organization_id_fkey,
  ADD CONSTRAINT platform_accounts_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE publications
  DROP CONSTRAINT publications_organization_id_fkey,
  ADD CONSTRAINT publications_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE oauth_connections
  DROP CONSTRAINT oauth_connections_organization_id_fkey,
  ADD CONSTRAINT oauth_connections_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE oauth_states
  DROP CONSTRAINT oauth_states_organization_id_fkey,
  ADD CONSTRAINT oauth_states_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE sync_targets
  DROP CONSTRAINT sync_targets_organization_id_fkey,
  ADD CONSTRAINT sync_targets_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE audit_logs
  DROP CONSTRAINT audit_logs_organization_id_fkey,
  ADD CONSTRAINT audit_logs_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;
ALTER TABLE creator_history_events
  DROP CONSTRAINT creator_history_events_organization_id_fkey,
  ADD CONSTRAINT creator_history_events_organization_id_fkey
    FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE;

ALTER TABLE creator_account_assignments
  DROP CONSTRAINT creator_account_assignments_assigned_by_fkey,
  ADD CONSTRAINT creator_account_assignments_assigned_by_fkey
    FOREIGN KEY (assigned_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE audit_logs
  DROP CONSTRAINT audit_logs_actor_id_fkey,
  ADD CONSTRAINT audit_logs_actor_id_fkey
    FOREIGN KEY (actor_id) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE creator_credentials
  DROP CONSTRAINT creator_credentials_updated_by_fkey,
  ADD CONSTRAINT creator_credentials_updated_by_fkey
    FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE content_groups
  DROP CONSTRAINT content_groups_created_by_fkey,
  ADD CONSTRAINT content_groups_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE user_invitations
  DROP CONSTRAINT user_invitations_created_by_fkey,
  ADD CONSTRAINT user_invitations_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE oauth_states
  DROP CONSTRAINT oauth_states_initiated_by_fkey,
  ADD CONSTRAINT oauth_states_initiated_by_fkey
    FOREIGN KEY (initiated_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE company_vk_accounts
  DROP CONSTRAINT company_vk_accounts_created_by_fkey,
  ADD CONSTRAINT company_vk_accounts_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
  DROP CONSTRAINT company_vk_accounts_updated_by_fkey,
  ADD CONSTRAINT company_vk_accounts_updated_by_fkey
    FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE creator_vk_assignments
  DROP CONSTRAINT creator_vk_assignments_updated_by_fkey,
  ADD CONSTRAINT creator_vk_assignments_updated_by_fkey
    FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE sync_runs
  DROP CONSTRAINT sync_runs_target_id_fkey,
  ADD CONSTRAINT sync_runs_target_id_fkey
    FOREIGN KEY (target_id) REFERENCES sync_targets(id) ON DELETE CASCADE;

-- Normalize legacy cross-workspace actors without deleting business history.
UPDATE oauth_states row SET initiated_by = NULL
WHERE initiated_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.initiated_by
);
DELETE FROM oauth_account_selections row
WHERE NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.initiated_by
);
UPDATE audit_logs row SET actor_id = NULL
WHERE actor_id IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.actor_id
);
UPDATE creator_history_events row SET actor_id = NULL
WHERE actor_id IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.actor_id
);
UPDATE user_invitations row SET created_by = NULL
WHERE created_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.created_by
);
UPDATE creators row SET created_by = NULL
WHERE created_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.created_by
);
UPDATE creator_account_assignments row SET assigned_by = NULL
WHERE assigned_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.assigned_by
);
UPDATE company_vk_accounts row SET created_by = NULL
WHERE created_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.created_by
);
UPDATE company_vk_accounts row SET updated_by = NULL
WHERE updated_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.updated_by
);
UPDATE creator_vk_assignments row SET updated_by = NULL
WHERE updated_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.updated_by
);
UPDATE manager_company_assignments row SET created_by = NULL
WHERE created_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM organization_memberships membership
  WHERE membership.organization_id=row.organization_id AND membership.user_id=row.created_by
);
UPDATE creator_credentials row SET updated_by = NULL
WHERE updated_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM creators creator
  JOIN organization_memberships membership
    ON membership.organization_id=creator.organization_id
   AND membership.user_id=row.updated_by
  WHERE creator.id=row.creator_id
);
UPDATE content_groups row SET created_by = NULL
WHERE created_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM creators creator
  JOIN organization_memberships membership
    ON membership.organization_id=creator.organization_id
   AND membership.user_id=row.created_by
  WHERE creator.id=row.creator_id
);
UPDATE manager_company_permissions row SET granted_by = NULL
WHERE granted_by IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM manager_company_assignments assignment
  JOIN organization_memberships membership
    ON membership.organization_id=assignment.organization_id
   AND membership.user_id=row.granted_by
  WHERE assignment.id=row.assignment_id
);

-- +goose StatementBegin
CREATE FUNCTION validate_workspace_user_reference() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  row_data jsonb := to_jsonb(NEW);
  workspace_id uuid;
  reference_user_id uuid;
BEGIN
  reference_user_id := NULLIF(row_data ->> TG_ARGV[1], '')::uuid;
  IF reference_user_id IS NULL THEN
    RETURN NEW;
  END IF;

  IF TG_ARGV[0] = 'organization' THEN
    workspace_id := NULLIF(row_data ->> 'organization_id', '')::uuid;
  ELSIF TG_ARGV[0] = 'creator' THEN
    SELECT creator.organization_id INTO workspace_id
    FROM creators creator
    WHERE creator.id = NULLIF(row_data ->> 'creator_id', '')::uuid;
  ELSIF TG_ARGV[0] = 'manager_assignment' THEN
    SELECT assignment.organization_id INTO workspace_id
    FROM manager_company_assignments assignment
    WHERE assignment.id = NULLIF(row_data ->> 'assignment_id', '')::uuid;
  END IF;

  IF workspace_id IS NULL THEN
    RAISE EXCEPTION 'user reference does not belong to the resource workspace'
      USING ERRCODE = '23514';
  END IF;

  PERFORM 1 FROM organization_memberships membership
    WHERE membership.organization_id = workspace_id
      AND membership.user_id = reference_user_id
    FOR KEY SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'user reference does not belong to the resource workspace'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER oauth_states_validate_initiated_by
  BEFORE INSERT OR UPDATE OF organization_id, initiated_by ON oauth_states
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'initiated_by');
CREATE TRIGGER oauth_account_selections_validate_initiated_by
  BEFORE INSERT OR UPDATE OF organization_id, initiated_by ON oauth_account_selections
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'initiated_by');
CREATE TRIGGER audit_logs_validate_actor
  BEFORE INSERT OR UPDATE OF organization_id, actor_id ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'actor_id');
CREATE TRIGGER creator_history_events_validate_actor
  BEFORE INSERT OR UPDATE OF organization_id, actor_id ON creator_history_events
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'actor_id');
CREATE TRIGGER user_invitations_validate_created_by
  BEFORE INSERT OR UPDATE OF organization_id, created_by ON user_invitations
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'created_by');
CREATE TRIGGER creators_validate_created_by
  BEFORE INSERT OR UPDATE OF organization_id, created_by ON creators
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'created_by');
CREATE TRIGGER creator_account_assignments_validate_assigned_by
  BEFORE INSERT OR UPDATE OF organization_id, assigned_by ON creator_account_assignments
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'assigned_by');
CREATE TRIGGER company_vk_accounts_validate_created_by
  BEFORE INSERT OR UPDATE OF organization_id, created_by ON company_vk_accounts
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'created_by');
CREATE TRIGGER company_vk_accounts_validate_updated_by
  BEFORE INSERT OR UPDATE OF organization_id, updated_by ON company_vk_accounts
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'updated_by');
CREATE TRIGGER creator_vk_assignments_validate_updated_by
  BEFORE INSERT OR UPDATE OF organization_id, updated_by ON creator_vk_assignments
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'updated_by');
CREATE TRIGGER manager_company_assignments_validate_created_by
  BEFORE INSERT OR UPDATE OF organization_id, created_by ON manager_company_assignments
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('organization', 'created_by');
CREATE TRIGGER creator_credentials_validate_updated_by
  BEFORE INSERT OR UPDATE OF creator_id, updated_by ON creator_credentials
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('creator', 'updated_by');
CREATE TRIGGER content_groups_validate_created_by
  BEFORE INSERT OR UPDATE OF creator_id, created_by ON content_groups
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('creator', 'created_by');
CREATE TRIGGER manager_company_permissions_validate_granted_by
  BEFORE INSERT OR UPDATE OF assignment_id, granted_by ON manager_company_permissions
  FOR EACH ROW EXECUTE FUNCTION validate_workspace_user_reference('manager_assignment', 'granted_by');

-- +goose StatementBegin
CREATE FUNCTION cleanup_workspace_user_references() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE oauth_states SET initiated_by=NULL
    WHERE organization_id=OLD.organization_id AND initiated_by=OLD.user_id;
  DELETE FROM oauth_account_selections
    WHERE organization_id=OLD.organization_id AND initiated_by=OLD.user_id;
  UPDATE audit_logs SET actor_id=NULL
    WHERE organization_id=OLD.organization_id AND actor_id=OLD.user_id;
  UPDATE creator_history_events SET actor_id=NULL
    WHERE organization_id=OLD.organization_id AND actor_id=OLD.user_id;
  UPDATE user_invitations SET created_by=NULL
    WHERE organization_id=OLD.organization_id AND created_by=OLD.user_id;
  UPDATE creators SET created_by=NULL
    WHERE organization_id=OLD.organization_id AND created_by=OLD.user_id;
  UPDATE creator_account_assignments SET assigned_by=NULL
    WHERE organization_id=OLD.organization_id AND assigned_by=OLD.user_id;
  UPDATE company_vk_accounts SET created_by=NULL
    WHERE organization_id=OLD.organization_id AND created_by=OLD.user_id;
  UPDATE company_vk_accounts SET updated_by=NULL
    WHERE organization_id=OLD.organization_id AND updated_by=OLD.user_id;
  UPDATE creator_vk_assignments SET updated_by=NULL
    WHERE organization_id=OLD.organization_id AND updated_by=OLD.user_id;
  UPDATE manager_company_assignments SET created_by=NULL
    WHERE organization_id=OLD.organization_id AND created_by=OLD.user_id;
  UPDATE creator_credentials credential SET updated_by=NULL
    FROM creators creator
    WHERE creator.id=credential.creator_id
      AND creator.organization_id=OLD.organization_id
      AND credential.updated_by=OLD.user_id;
  UPDATE content_groups content_group SET created_by=NULL
    FROM creators creator
    WHERE creator.id=content_group.creator_id
      AND creator.organization_id=OLD.organization_id
      AND content_group.created_by=OLD.user_id;
  UPDATE manager_company_permissions permission SET granted_by=NULL
    FROM manager_company_assignments assignment
    WHERE assignment.id=permission.assignment_id
      AND assignment.organization_id=OLD.organization_id
      AND permission.granted_by=OLD.user_id;
  UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()),
      active_company_id=NULL,active_creator_id=NULL
    WHERE user_id=OLD.user_id AND revoked_at IS NULL;
  RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER organization_memberships_cleanup_user_references
  AFTER DELETE ON organization_memberships
  FOR EACH ROW EXECUTE FUNCTION cleanup_workspace_user_references();

-- PostgreSQL does not create indexes for referencing columns.  These indexes
-- keep workspace cascades, user SET NULL actions, and scoped joins bounded.
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX creators_created_by_idx ON creators (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX creator_contacts_creator_idx ON creator_contacts (creator_id);
CREATE INDEX creator_account_assignments_creator_idx
  ON creator_account_assignments (creator_id, organization_id);
CREATE INDEX creator_account_assignments_assigned_by_idx
  ON creator_account_assignments (assigned_by) WHERE assigned_by IS NOT NULL;
CREATE INDEX publications_platform_account_idx
  ON publications (platform_account_id, organization_id);
CREATE INDEX publications_creator_workspace_idx
  ON publications (creator_id, organization_id);
CREATE INDEX sync_runs_target_idx ON sync_runs (target_id) WHERE target_id IS NOT NULL;
CREATE INDEX audit_logs_organization_idx ON audit_logs (organization_id, created_at DESC);
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX content_groups_creator_idx ON content_groups (creator_id);
CREATE INDEX content_groups_created_by_idx ON content_groups (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX content_match_suggestions_creator_idx ON content_match_suggestions (creator_id);
CREATE INDEX content_match_suggestions_publication_b_idx
  ON content_match_suggestions (publication_b_id);
CREATE INDEX oauth_connections_organization_idx ON oauth_connections (organization_id);
CREATE INDEX oauth_connections_account_workspace_idx
  ON oauth_connections (platform_account_id, organization_id);
CREATE INDEX oauth_states_organization_idx ON oauth_states (organization_id, expires_at);
CREATE INDEX oauth_states_creator_idx
  ON oauth_states (creator_id, organization_id) WHERE creator_id IS NOT NULL;
CREATE INDEX oauth_states_company_vk_account_idx
  ON oauth_states (company_vk_account_id, organization_id) WHERE company_vk_account_id IS NOT NULL;
CREATE INDEX oauth_states_initiated_by_idx ON oauth_states (initiated_by) WHERE initiated_by IS NOT NULL;
CREATE INDEX user_invitations_organization_idx ON user_invitations (organization_id);
CREATE INDEX user_invitations_created_by_idx
  ON user_invitations (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX creator_credentials_updated_by_idx
  ON creator_credentials (updated_by) WHERE updated_by IS NOT NULL;
CREATE INDEX platform_data_deletion_requests_organization_idx
  ON platform_data_deletion_requests (organization_id) WHERE organization_id IS NOT NULL;
CREATE INDEX platform_data_deletion_requests_platform_account_idx
  ON platform_data_deletion_requests (platform_account_id) WHERE platform_account_id IS NOT NULL;
CREATE INDEX creator_history_events_organization_idx
  ON creator_history_events (organization_id, created_at DESC);
CREATE INDEX creator_history_events_creator_workspace_idx
  ON creator_history_events (creator_id, organization_id);
CREATE INDEX creator_history_events_actor_idx
  ON creator_history_events (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX company_vk_accounts_created_by_idx
  ON company_vk_accounts (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX company_vk_accounts_updated_by_idx
  ON company_vk_accounts (updated_by) WHERE updated_by IS NOT NULL;
CREATE INDEX company_vk_accounts_company_workspace_idx
  ON company_vk_accounts (company_id, organization_id);
CREATE INDEX company_vk_accounts_platform_workspace_idx
  ON company_vk_accounts (platform_account_id, organization_id)
  WHERE platform_account_id IS NOT NULL;
CREATE INDEX creator_vk_assignments_updated_by_idx
  ON creator_vk_assignments (updated_by) WHERE updated_by IS NOT NULL;
CREATE INDEX creator_vk_assignments_creator_workspace_idx
  ON creator_vk_assignments (creator_id, organization_id);
CREATE INDEX creator_vk_assignments_account_workspace_idx
  ON creator_vk_assignments (company_vk_account_id, organization_id);
CREATE INDEX oauth_account_selections_organization_idx
  ON oauth_account_selections (organization_id, expires_at);
CREATE INDEX oauth_account_selections_creator_workspace_idx
  ON oauth_account_selections (creator_id, organization_id);
CREATE INDEX oauth_account_selections_initiated_by_idx
  ON oauth_account_selections (initiated_by);
CREATE INDEX sync_targets_organization_idx ON sync_targets (organization_id);
CREATE INDEX manager_company_assignments_created_by_idx
  ON manager_company_assignments (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX manager_company_permissions_granted_by_idx
  ON manager_company_permissions (granted_by) WHERE granted_by IS NOT NULL;

-- +goose Down
DROP TRIGGER IF EXISTS organization_memberships_cleanup_user_references ON organization_memberships;
DROP FUNCTION IF EXISTS cleanup_workspace_user_references();
DROP TRIGGER IF EXISTS manager_company_permissions_validate_granted_by ON manager_company_permissions;
DROP TRIGGER IF EXISTS content_groups_validate_created_by ON content_groups;
DROP TRIGGER IF EXISTS creator_credentials_validate_updated_by ON creator_credentials;
DROP TRIGGER IF EXISTS manager_company_assignments_validate_created_by ON manager_company_assignments;
DROP TRIGGER IF EXISTS creator_vk_assignments_validate_updated_by ON creator_vk_assignments;
DROP TRIGGER IF EXISTS company_vk_accounts_validate_updated_by ON company_vk_accounts;
DROP TRIGGER IF EXISTS company_vk_accounts_validate_created_by ON company_vk_accounts;
DROP TRIGGER IF EXISTS creator_account_assignments_validate_assigned_by ON creator_account_assignments;
DROP TRIGGER IF EXISTS creators_validate_created_by ON creators;
DROP TRIGGER IF EXISTS user_invitations_validate_created_by ON user_invitations;
DROP TRIGGER IF EXISTS creator_history_events_validate_actor ON creator_history_events;
DROP TRIGGER IF EXISTS audit_logs_validate_actor ON audit_logs;
DROP TRIGGER IF EXISTS oauth_account_selections_validate_initiated_by ON oauth_account_selections;
DROP TRIGGER IF EXISTS oauth_states_validate_initiated_by ON oauth_states;
DROP FUNCTION IF EXISTS validate_workspace_user_reference();

DROP INDEX IF EXISTS manager_company_permissions_granted_by_idx;
DROP INDEX IF EXISTS manager_company_assignments_created_by_idx;
DROP INDEX IF EXISTS sync_targets_organization_idx;
DROP INDEX IF EXISTS oauth_account_selections_initiated_by_idx;
DROP INDEX IF EXISTS oauth_account_selections_creator_workspace_idx;
DROP INDEX IF EXISTS oauth_account_selections_organization_idx;
DROP INDEX IF EXISTS creator_vk_assignments_updated_by_idx;
DROP INDEX IF EXISTS creator_vk_assignments_account_workspace_idx;
DROP INDEX IF EXISTS creator_vk_assignments_creator_workspace_idx;
DROP INDEX IF EXISTS company_vk_accounts_updated_by_idx;
DROP INDEX IF EXISTS company_vk_accounts_created_by_idx;
DROP INDEX IF EXISTS company_vk_accounts_platform_workspace_idx;
DROP INDEX IF EXISTS company_vk_accounts_company_workspace_idx;
DROP INDEX IF EXISTS creator_history_events_actor_idx;
DROP INDEX IF EXISTS creator_history_events_organization_idx;
DROP INDEX IF EXISTS creator_history_events_creator_workspace_idx;
DROP INDEX IF EXISTS platform_data_deletion_requests_platform_account_idx;
DROP INDEX IF EXISTS platform_data_deletion_requests_organization_idx;
DROP INDEX IF EXISTS creator_credentials_updated_by_idx;
DROP INDEX IF EXISTS user_invitations_created_by_idx;
DROP INDEX IF EXISTS user_invitations_organization_idx;
DROP INDEX IF EXISTS oauth_states_initiated_by_idx;
DROP INDEX IF EXISTS oauth_states_company_vk_account_idx;
DROP INDEX IF EXISTS oauth_states_creator_idx;
DROP INDEX IF EXISTS oauth_states_organization_idx;
DROP INDEX IF EXISTS oauth_connections_organization_idx;
DROP INDEX IF EXISTS oauth_connections_account_workspace_idx;
DROP INDEX IF EXISTS content_match_suggestions_creator_idx;
DROP INDEX IF EXISTS content_match_suggestions_publication_b_idx;
DROP INDEX IF EXISTS content_groups_created_by_idx;
DROP INDEX IF EXISTS content_groups_creator_idx;
DROP INDEX IF EXISTS audit_logs_actor_idx;
DROP INDEX IF EXISTS audit_logs_organization_idx;
DROP INDEX IF EXISTS sync_runs_target_idx;
DROP INDEX IF EXISTS publications_platform_account_idx;
DROP INDEX IF EXISTS publications_creator_workspace_idx;
DROP INDEX IF EXISTS creator_account_assignments_assigned_by_idx;
DROP INDEX IF EXISTS creator_account_assignments_creator_idx;
DROP INDEX IF EXISTS creator_account_assignments_account_workspace_idx;
DROP INDEX IF EXISTS creator_contacts_creator_idx;
DROP INDEX IF EXISTS creators_created_by_idx;
DROP INDEX IF EXISTS sessions_user_idx;

ALTER TABLE sync_runs
  DROP CONSTRAINT sync_runs_target_id_fkey,
  ADD CONSTRAINT sync_runs_target_id_fkey FOREIGN KEY (target_id) REFERENCES sync_targets(id);
ALTER TABLE creator_vk_assignments
  DROP CONSTRAINT creator_vk_assignments_updated_by_fkey,
  ADD CONSTRAINT creator_vk_assignments_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES users(id);
ALTER TABLE company_vk_accounts
  DROP CONSTRAINT company_vk_accounts_created_by_fkey,
  ADD CONSTRAINT company_vk_accounts_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id),
  DROP CONSTRAINT company_vk_accounts_updated_by_fkey,
  ADD CONSTRAINT company_vk_accounts_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES users(id);
ALTER TABLE oauth_states
  DROP CONSTRAINT oauth_states_initiated_by_fkey,
  ADD CONSTRAINT oauth_states_initiated_by_fkey FOREIGN KEY (initiated_by) REFERENCES users(id);
ALTER TABLE user_invitations
  DROP CONSTRAINT user_invitations_created_by_fkey,
  ADD CONSTRAINT user_invitations_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);
ALTER TABLE content_groups
  DROP CONSTRAINT content_groups_created_by_fkey,
  ADD CONSTRAINT content_groups_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);
ALTER TABLE creator_credentials
  DROP CONSTRAINT creator_credentials_updated_by_fkey,
  ADD CONSTRAINT creator_credentials_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES users(id);
ALTER TABLE audit_logs
  DROP CONSTRAINT audit_logs_actor_id_fkey,
  ADD CONSTRAINT audit_logs_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES users(id);
ALTER TABLE creator_account_assignments
  DROP CONSTRAINT creator_account_assignments_assigned_by_fkey,
  ADD CONSTRAINT creator_account_assignments_assigned_by_fkey FOREIGN KEY (assigned_by) REFERENCES users(id);

ALTER TABLE creator_history_events
  DROP CONSTRAINT creator_history_events_organization_id_fkey,
  ADD CONSTRAINT creator_history_events_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE audit_logs
  DROP CONSTRAINT audit_logs_organization_id_fkey,
  ADD CONSTRAINT audit_logs_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE sync_targets
  DROP CONSTRAINT sync_targets_organization_id_fkey,
  ADD CONSTRAINT sync_targets_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE oauth_states
  DROP CONSTRAINT oauth_states_organization_id_fkey,
  ADD CONSTRAINT oauth_states_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE oauth_connections
  DROP CONSTRAINT oauth_connections_organization_id_fkey,
  ADD CONSTRAINT oauth_connections_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE publications
  DROP CONSTRAINT publications_organization_id_fkey,
  ADD CONSTRAINT publications_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE platform_accounts
  DROP CONSTRAINT platform_accounts_organization_id_fkey,
  ADD CONSTRAINT platform_accounts_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);
ALTER TABLE creators
  DROP CONSTRAINT creators_organization_id_fkey,
  ADD CONSTRAINT creators_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organizations(id);

DROP TRIGGER IF EXISTS platform_data_deletion_requests_validate_scope ON platform_data_deletion_requests;
DROP FUNCTION IF EXISTS validate_platform_deletion_request_scope();
DROP TRIGGER IF EXISTS content_match_suggestions_validate_scope ON content_match_suggestions;
DROP FUNCTION IF EXISTS validate_content_match_suggestion_scope();
DROP TRIGGER IF EXISTS content_group_members_validate_scope ON content_group_members;
DROP FUNCTION IF EXISTS validate_content_group_member_scope();
DROP TRIGGER IF EXISTS content_groups_creator_immutable ON content_groups;
DROP FUNCTION IF EXISTS prevent_content_group_creator_move();
DROP TRIGGER IF EXISTS creator_vk_assignments_validate_scope ON creator_vk_assignments;
DROP FUNCTION IF EXISTS validate_creator_vk_assignment_scope();
ALTER TABLE creator_vk_assignments
  DROP CONSTRAINT creator_vk_assignments_creator_workspace_fkey,
  ADD CONSTRAINT creator_vk_assignments_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE,
  DROP CONSTRAINT creator_vk_assignments_account_workspace_fkey,
  ADD CONSTRAINT creator_vk_assignments_company_vk_account_id_fkey
    FOREIGN KEY (company_vk_account_id) REFERENCES company_vk_accounts(id) ON DELETE CASCADE,
  DROP COLUMN organization_id;

ALTER TABLE creator_history_events
  DROP CONSTRAINT creator_history_events_creator_workspace_fkey,
  ADD CONSTRAINT creator_history_events_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;
ALTER TABLE oauth_account_selections
  DROP CONSTRAINT oauth_account_selections_creator_workspace_fkey,
  ADD CONSTRAINT oauth_account_selections_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE;
ALTER TABLE oauth_states
  DROP CONSTRAINT oauth_states_creator_workspace_fkey,
  ADD CONSTRAINT oauth_states_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE,
  DROP CONSTRAINT oauth_states_company_vk_workspace_fkey,
  ADD CONSTRAINT oauth_states_company_vk_account_id_fkey
    FOREIGN KEY (company_vk_account_id) REFERENCES company_vk_accounts(id) ON DELETE CASCADE;
ALTER TABLE oauth_connections
  DROP CONSTRAINT oauth_connections_account_workspace_fkey,
  ADD CONSTRAINT oauth_connections_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id) ON DELETE CASCADE;
ALTER TABLE company_vk_accounts
  DROP CONSTRAINT company_vk_accounts_company_workspace_fkey,
  ADD CONSTRAINT company_vk_accounts_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
  DROP CONSTRAINT company_vk_accounts_platform_account_workspace_fkey,
  ADD CONSTRAINT company_vk_accounts_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id) ON DELETE SET NULL;
DROP TRIGGER IF EXISTS company_vk_accounts_validate_scope ON company_vk_accounts;
DROP FUNCTION IF EXISTS validate_company_vk_account_scope();
ALTER TABLE company_vk_accounts DROP CONSTRAINT IF EXISTS company_vk_accounts_id_organization_key;

ALTER TABLE publications
  DROP CONSTRAINT publications_creator_workspace_fkey,
  ADD CONSTRAINT publications_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE,
  DROP CONSTRAINT publications_account_workspace_fkey,
  ADD CONSTRAINT publications_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id) ON DELETE CASCADE;
DROP TRIGGER IF EXISTS publications_validate_scope ON publications;
DROP FUNCTION IF EXISTS validate_publication_scope();

DROP INDEX IF EXISTS sync_targets_company_workspace_idx;
DROP TRIGGER IF EXISTS creators_delete_sync_targets ON creators;
DROP TRIGGER IF EXISTS platform_accounts_delete_sync_targets ON platform_accounts;
DROP FUNCTION IF EXISTS delete_polymorphic_sync_targets();
DROP TRIGGER IF EXISTS sync_targets_scope ON sync_targets;
DROP FUNCTION IF EXISTS scope_sync_target();
ALTER TABLE sync_targets DROP CONSTRAINT IF EXISTS sync_targets_company_workspace_fkey;
ALTER TABLE sync_targets DROP COLUMN IF EXISTS company_id;

DROP TRIGGER IF EXISTS creator_account_assignments_scope ON creator_account_assignments;
DROP FUNCTION IF EXISTS scope_creator_account_assignment();
ALTER TABLE creator_account_assignments
  DROP CONSTRAINT IF EXISTS creator_account_assignments_account_workspace_fkey,
  DROP CONSTRAINT IF EXISTS creator_account_assignments_creator_workspace_fkey,
  DROP COLUMN IF EXISTS organization_id,
  ADD CONSTRAINT creator_account_assignments_creator_id_fkey
    FOREIGN KEY (creator_id) REFERENCES creators(id) ON DELETE CASCADE,
  ADD CONSTRAINT creator_account_assignments_platform_account_id_fkey
    FOREIGN KEY (platform_account_id) REFERENCES platform_accounts(id) ON DELETE CASCADE;

DROP INDEX IF EXISTS platform_accounts_company_workspace_idx;
DROP TRIGGER IF EXISTS platform_accounts_cascade_sync_scope ON platform_accounts;
DROP FUNCTION IF EXISTS cascade_platform_account_company_to_sync_targets();
DROP TRIGGER IF EXISTS platform_accounts_scope ON platform_accounts;
DROP FUNCTION IF EXISTS scope_platform_account();
ALTER TABLE platform_accounts DROP CONSTRAINT IF EXISTS platform_accounts_company_workspace_fkey;
ALTER TABLE platform_accounts DROP CONSTRAINT IF EXISTS platform_accounts_id_organization_key;
ALTER TABLE platform_accounts DROP COLUMN IF EXISTS company_id;

DROP TRIGGER IF EXISTS companies_invalidate_sessions ON companies;
DROP TRIGGER IF EXISTS creators_invalidate_sessions ON creators;
DROP TRIGGER IF EXISTS manager_company_assignments_identity_immutable ON manager_company_assignments;
DROP FUNCTION IF EXISTS prevent_manager_assignment_identity_move();
DROP TRIGGER IF EXISTS manager_company_assignments_invalidate_sessions ON manager_company_assignments;
DROP FUNCTION IF EXISTS invalidate_deleted_session_context();
DROP TRIGGER IF EXISTS organization_memberships_invalidate_changed_sessions ON organization_memberships;
DROP FUNCTION IF EXISTS invalidate_changed_membership_sessions();
DROP TABLE IF EXISTS manager_company_permissions;
DROP TABLE IF EXISTS manager_company_assignments;

DROP TRIGGER IF EXISTS sessions_validate_context ON sessions;
DROP FUNCTION IF EXISTS validate_session_context();
DROP INDEX IF EXISTS sessions_active_creator_idx;
DROP INDEX IF EXISTS sessions_active_company_idx;
ALTER TABLE sessions DROP COLUMN IF EXISTS active_creator_id;
ALTER TABLE sessions DROP COLUMN IF EXISTS active_company_id;

DROP INDEX IF EXISTS creators_one_profile_per_user_company_idx;
DROP INDEX IF EXISTS creators_login_user_idx;
DROP INDEX IF EXISTS creators_login_workspace_idx;
DROP INDEX IF EXISTS creators_company_workspace_idx;
DROP INDEX IF EXISTS creators_organization_company_idx;
DROP TRIGGER IF EXISTS creators_company_immutable ON creators;
DROP FUNCTION IF EXISTS prevent_creator_company_move();
DROP TRIGGER IF EXISTS organization_memberships_protect_creator_role ON organization_memberships;
DROP FUNCTION IF EXISTS protect_creator_membership_role();
DROP TRIGGER IF EXISTS creators_validate_login_membership ON creators;
DROP FUNCTION IF EXISTS validate_creator_login_membership();
ALTER TABLE creators DROP CONSTRAINT IF EXISTS creators_login_membership_fkey;
ALTER TABLE creators DROP CONSTRAINT IF EXISTS creators_company_workspace_fkey;
ALTER TABLE creators DROP CONSTRAINT IF EXISTS creators_id_organization_key;
ALTER TABLE creators ALTER COLUMN company_id DROP NOT NULL;
ALTER TABLE creators
  ADD CONSTRAINT creators_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE SET NULL;
ALTER TABLE creators DROP COLUMN IF EXISTS login_user_id;

DROP INDEX IF EXISTS companies_lifecycle_purge_idx;
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_lifecycle_check;
DROP TRIGGER IF EXISTS companies_normalize_lifecycle ON companies;
DROP FUNCTION IF EXISTS normalize_company_lifecycle();
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_id_organization_key;
ALTER TABLE companies DROP COLUMN IF EXISTS purge_at;
ALTER TABLE companies DROP COLUMN IF EXISTS lifecycle_state;

DROP INDEX IF EXISTS organization_memberships_workspace_role_idx;
DROP TRIGGER IF EXISTS organization_memberships_identity_immutable ON organization_memberships;
DROP FUNCTION IF EXISTS prevent_membership_identity_move();
DROP TRIGGER IF EXISTS organization_memberships_synchronize_roles ON organization_memberships;
DROP FUNCTION IF EXISTS synchronize_membership_roles();
ALTER TABLE organization_memberships DROP CONSTRAINT IF EXISTS organization_memberships_one_workspace_key;
ALTER TABLE organization_memberships DROP CONSTRAINT IF EXISTS organization_memberships_workspace_role_key;
ALTER TABLE organization_memberships DROP COLUMN IF EXISTS membership_role;

DROP TYPE IF EXISTS company_lifecycle_state;
DROP TYPE IF EXISTS manager_permission;
DROP TYPE IF EXISTS membership_role;
