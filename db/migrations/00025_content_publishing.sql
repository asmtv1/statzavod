-- +goose Up
-- OAuth reconnect generations make the provider exchange/save boundary a CAS:
-- a newer authorization state for the same credential invalidates every older
-- callback before it can replace encrypted tokens or wake publishing jobs.
ALTER TABLE oauth_connections
  ADD COLUMN authorization_generation bigint NOT NULL DEFAULT 0 CHECK (authorization_generation >= 0);
ALTER TABLE oauth_states
  ADD COLUMN reconnect_platform_account_id uuid,
  ADD COLUMN authorization_generation bigint,
  ADD CONSTRAINT oauth_states_reconnect_generation_check CHECK (
    (reconnect_platform_account_id IS NULL AND authorization_generation IS NULL)
    OR (reconnect_platform_account_id IS NOT NULL AND authorization_generation > 0)
  ),
  ADD CONSTRAINT oauth_states_reconnect_account_workspace_fkey
    FOREIGN KEY (reconnect_platform_account_id, organization_id)
    REFERENCES platform_accounts(id, organization_id) ON DELETE CASCADE;
CREATE INDEX oauth_states_reconnect_generation_idx
  ON oauth_states (reconnect_platform_account_id, authorization_generation)
  WHERE reconnect_platform_account_id IS NOT NULL;

-- This key lets child content rows carry the creator's company in their FK,
-- rather than relying on a same-workspace creator id alone.
ALTER TABLE creators ADD CONSTRAINT creators_id_company_workspace_key UNIQUE (id, company_id, organization_id);

CREATE TABLE content_items (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id),
  company_id uuid NOT NULL,
  creator_id uuid NOT NULL,
  created_by uuid NOT NULL REFERENCES users(id),
  current_revision integer NOT NULL DEFAULT 1 CHECK (current_revision > 0),
  cancelled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, organization_id),
  FOREIGN KEY (company_id, organization_id) REFERENCES companies(id, organization_id),
  FOREIGN KEY (creator_id, company_id, organization_id) REFERENCES creators(id, company_id, organization_id)
);
CREATE INDEX content_items_scope_updated_idx ON content_items (organization_id, company_id, creator_id, updated_at DESC, id DESC);
CREATE INDEX content_items_owner_updated_idx ON content_items (organization_id, updated_at DESC, id DESC) WHERE cancelled_at IS NULL;
CREATE INDEX content_items_company_updated_idx ON content_items (organization_id, company_id, updated_at DESC, id DESC) WHERE cancelled_at IS NULL;

CREATE TABLE content_revisions (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  content_item_id uuid NOT NULL REFERENCES content_items(id) ON DELETE CASCADE,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  revision integer NOT NULL CHECK (revision > 0),
  description text NOT NULL DEFAULT '',
  hashtags text[] NOT NULL DEFAULT '{}',
  platform_options jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(platform_options)='object'),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','PENDING_APPROVAL','APPROVED','SCHEDULED','PUBLISHING','PARTIALLY_PUBLISHED','PUBLISHED','FAILED','CANCELLED')),
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (content_item_id, revision),
  UNIQUE (id, organization_id)
);
CREATE INDEX content_revisions_item_current_idx ON content_revisions (content_item_id, revision DESC);

CREATE TABLE content_publish_targets (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  content_revision_id uuid NOT NULL,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  company_id uuid NOT NULL,
  creator_id uuid NOT NULL,
  platform_account_id uuid NOT NULL,
  platform platform NOT NULL,
  media_asset_id uuid,
  platform_options jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(platform_options)='object'),
  sent_snapshot jsonb,
  status text NOT NULL DEFAULT 'READY' CHECK (status IN ('WAITING_APPROVAL','SCHEDULED','READY','CLAIMED','UPLOADING','PROCESSING','PUBLISHING','SUCCEEDED','RETRY_SCHEDULED','WAITING_FOR_REAUTH','FAILED','SKIPPED_INCOMPATIBLE','CANCELLED')),
  provider_operation_id text,
  -- Provider-issued resumable/upload capability URLs are secrets.  Keep only
  -- an opaque phase/reference in provider_operation_id and protect the bound
  -- resume payload with the application's authenticated encryption envelope.
  provider_resume_ciphertext bytea,
  provider_resume_nonce bytea,
  provider_resume_key_version smallint,
  external_id text,
  external_url text,
  error_code text,
  error_message text,
  scheduled_at timestamptz,
  cancellation_requested_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (content_revision_id, platform_account_id),
  UNIQUE (id, organization_id),
  FOREIGN KEY (content_revision_id, organization_id) REFERENCES content_revisions(id, organization_id),
  FOREIGN KEY (company_id, organization_id) REFERENCES companies(id, organization_id),
  FOREIGN KEY (creator_id, company_id, organization_id) REFERENCES creators(id, company_id, organization_id),
  FOREIGN KEY (platform_account_id, organization_id) REFERENCES platform_accounts(id, organization_id),
  CHECK ((provider_resume_ciphertext IS NULL AND provider_resume_nonce IS NULL AND provider_resume_key_version IS NULL)
      OR (provider_resume_ciphertext IS NOT NULL AND provider_resume_nonce IS NOT NULL AND provider_resume_key_version = 1)),
  CHECK ((status <> 'SCHEDULED' AND scheduled_at IS NULL) OR scheduled_at IS NOT NULL)
);
CREATE INDEX content_publish_targets_scope_status_idx ON content_publish_targets (organization_id, company_id, creator_id, status, scheduled_at, id);
CREATE INDEX content_publish_targets_attention_idx ON content_publish_targets (organization_id, company_id, updated_at DESC) WHERE status IN ('FAILED','WAITING_FOR_REAUTH','RETRY_SCHEDULED');

CREATE TABLE content_approval_requests (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  content_item_id uuid NOT NULL REFERENCES content_items(id) ON DELETE CASCADE,
  content_revision_id uuid NOT NULL,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  requested_by uuid NOT NULL REFERENCES users(id),
  decided_by uuid REFERENCES users(id),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','APPROVED','REJECTED','SUPERSEDED','CANCELLED')),
  decision_note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  FOREIGN KEY (content_revision_id, organization_id) REFERENCES content_revisions(id, organization_id),
  UNIQUE (id, organization_id),
  CHECK (decided_by IS NULL OR decided_by <> requested_by)
);
CREATE UNIQUE INDEX content_approval_one_pending_idx ON content_approval_requests (content_item_id, content_revision_id) WHERE status='PENDING';
CREATE INDEX content_approval_pending_scope_idx ON content_approval_requests (organization_id, created_at, id) WHERE status='PENDING';

CREATE TABLE content_publish_jobs (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  target_id uuid NOT NULL,
  content_revision_id uuid NOT NULL,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  company_id uuid NOT NULL,
  run_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'READY' CHECK (status IN ('READY','RUNNING','RETRY_SCHEDULED','CANCELLED','SUCCEEDED','FAILED')),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 5),
  execution_count integer NOT NULL DEFAULT 0 CHECK (execution_count >= 0),
  locked_by text,
  locked_at timestamptz,
  lease_expires_at timestamptz,
  reauth_deadline_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, organization_id),
  FOREIGN KEY (target_id, organization_id) REFERENCES content_publish_targets(id, organization_id),
  FOREIGN KEY (content_revision_id, organization_id) REFERENCES content_revisions(id, organization_id),
  FOREIGN KEY (company_id, organization_id) REFERENCES companies(id, organization_id)
);
CREATE INDEX content_publish_jobs_due_idx ON content_publish_jobs (run_at, id) WHERE status IN ('READY','RETRY_SCHEDULED');
CREATE UNIQUE INDEX content_publish_jobs_one_active_target_idx ON content_publish_jobs (target_id) WHERE status IN ('READY','RUNNING','RETRY_SCHEDULED');
CREATE INDEX content_publish_jobs_reauth_deadline_idx ON content_publish_jobs (reauth_deadline_at, id) WHERE status='RETRY_SCHEDULED' AND reauth_deadline_at IS NOT NULL;
CREATE INDEX content_publish_jobs_expired_lease_idx ON content_publish_jobs (lease_expires_at, id) WHERE status='RUNNING';

CREATE TABLE content_publish_attempts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  job_id uuid NOT NULL REFERENCES content_publish_jobs(id) ON DELETE CASCADE,
  target_id uuid NOT NULL,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  attempt_number integer NOT NULL CHECK (attempt_number > 0),
  status text NOT NULL CHECK (status IN ('RUNNING','PROCESSING','SUCCEEDED','FAILED','CANCELLED','WAITING_FOR_REAUTH')),
  provider_operation_id text,
  error_code text,
  error_message text,
  provider_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  UNIQUE (job_id, attempt_number),
  FOREIGN KEY (target_id, organization_id) REFERENCES content_publish_targets(id, organization_id)
);
CREATE INDEX content_publish_attempts_target_idx ON content_publish_attempts (target_id, started_at DESC);

CREATE TABLE content_notifications (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id),
  company_id uuid NOT NULL,
  recipient_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  content_item_id uuid REFERENCES content_items(id) ON DELETE CASCADE,
  kind text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (company_id, organization_id) REFERENCES companies(id, organization_id)
);
CREATE INDEX content_notifications_unread_idx ON content_notifications (recipient_id, created_at DESC) WHERE read_at IS NULL;

CREATE TABLE content_command_idempotency (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id),
  actor_id uuid NOT NULL REFERENCES users(id),
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 255),
  request_hash bytea NOT NULL,
  response_status integer NOT NULL,
  response_body bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
  UNIQUE (organization_id, actor_id, idempotency_key)
);
CREATE INDEX content_command_idempotency_expiry_idx ON content_command_idempotency (expires_at);

ALTER TABLE publications
  ADD COLUMN content_item_id uuid REFERENCES content_items(id) ON DELETE SET NULL,
  ADD COLUMN content_publish_target_id uuid REFERENCES content_publish_targets(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX publications_content_target_idx ON publications (content_publish_target_id) WHERE content_publish_target_id IS NOT NULL;

-- Successful provider publication facts are analytics, not mutable publishing
-- state. Company purge detaches their tenant-owned dimensions instead of
-- cascading the historical fact away.
ALTER TABLE publications
  ALTER COLUMN creator_id DROP NOT NULL,
  ALTER COLUMN platform_account_id DROP NOT NULL,
  DROP CONSTRAINT publications_creator_workspace_fkey,
  ADD CONSTRAINT publications_creator_workspace_fkey
    FOREIGN KEY (creator_id, organization_id) REFERENCES creators(id, organization_id) ON DELETE SET NULL (creator_id),
  DROP CONSTRAINT publications_account_workspace_fkey,
  ADD CONSTRAINT publications_account_workspace_fkey
    FOREIGN KEY (platform_account_id, organization_id) REFERENCES platform_accounts(id, organization_id) ON DELETE SET NULL (platform_account_id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION validate_publication_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE creator_workspace uuid; creator_company uuid; account_workspace uuid; account_company uuid;
BEGIN
  IF TG_OP='INSERT' AND (NEW.creator_id IS NULL OR NEW.platform_account_id IS NULL) THEN
    RAISE EXCEPTION 'new publication requires creator and platform account' USING ERRCODE='23503';
  END IF;
  IF TG_OP='UPDATE' AND (
    NEW.organization_id IS DISTINCT FROM OLD.organization_id
    OR (NEW.creator_id IS DISTINCT FROM OLD.creator_id AND NOT (OLD.creator_id IS NOT NULL AND NEW.creator_id IS NULL))
    OR (NEW.platform_account_id IS DISTINCT FROM OLD.platform_account_id AND NOT (OLD.platform_account_id IS NOT NULL AND NEW.platform_account_id IS NULL))
  ) THEN
    RAISE EXCEPTION 'publication ownership cannot be changed' USING ERRCODE='23514';
  END IF;
  IF NEW.creator_id IS NULL OR NEW.platform_account_id IS NULL THEN RETURN NEW; END IF;
  SELECT creator.organization_id,creator.company_id,account.organization_id,account.company_id
    INTO creator_workspace,creator_company,account_workspace,account_company
  FROM creators creator CROSS JOIN platform_accounts account
  WHERE creator.id=NEW.creator_id AND account.id=NEW.platform_account_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'publication creator or platform account does not exist' USING ERRCODE='23503'; END IF;
  IF NEW.organization_id<>creator_workspace OR NEW.organization_id<>account_workspace OR creator_company<>account_company THEN
    RAISE EXCEPTION 'publication resources belong to a different scope' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION validate_content_item_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE creator_company uuid;
BEGIN
  SELECT company_id INTO creator_company
  FROM creators
  WHERE id=NEW.creator_id AND organization_id=NEW.organization_id;
  IF creator_company IS NULL OR creator_company <> NEW.company_id THEN
    RAISE EXCEPTION 'content item creator does not belong to its company'
      USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER content_items_validate_scope
  BEFORE INSERT OR UPDATE OF organization_id, company_id, creator_id ON content_items
  FOR EACH ROW EXECUTE FUNCTION validate_content_item_scope();

-- +goose StatementBegin
CREATE FUNCTION validate_content_publish_target_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE revision_item uuid; item_company uuid; item_creator uuid; account_platform platform; account_company uuid;
BEGIN
  SELECT content_item_id INTO revision_item FROM content_revisions WHERE id=NEW.content_revision_id AND organization_id=NEW.organization_id;
  SELECT company_id,creator_id INTO item_company,item_creator FROM content_items WHERE id=revision_item AND organization_id=NEW.organization_id;
  SELECT platform,company_id INTO account_platform,account_company FROM platform_accounts WHERE id=NEW.platform_account_id AND organization_id=NEW.organization_id;
  IF revision_item IS NULL OR item_company <> NEW.company_id OR item_creator <> NEW.creator_id OR account_platform <> NEW.platform OR account_company <> NEW.company_id
     OR NOT (
       EXISTS (
         SELECT 1 FROM creator_account_assignments assignment
         WHERE assignment.creator_id=NEW.creator_id
           AND assignment.platform_account_id=NEW.platform_account_id
           AND assignment.organization_id=NEW.organization_id
           AND assignment.valid_to IS NULL
       )
       -- A company VK account is intentionally company-owned. It may publish
       -- for any target creator in that same company, but never across scope.
       OR (NEW.platform='VK' AND EXISTS (
         SELECT 1 FROM company_vk_accounts vk
         WHERE vk.platform_account_id=NEW.platform_account_id
           AND vk.organization_id=NEW.organization_id
           AND vk.company_id=NEW.company_id
       ))
     ) THEN
    RAISE EXCEPTION 'content target resources cross tenant, company, creator, or platform' USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER content_publish_targets_validate_scope BEFORE INSERT OR UPDATE OF content_revision_id, organization_id, company_id, creator_id, platform_account_id, platform ON content_publish_targets FOR EACH ROW EXECUTE FUNCTION validate_content_publish_target_scope();

CREATE INDEX creator_account_assignments_active_creator_account_scope_idx
  ON creator_account_assignments (creator_id, platform_account_id, organization_id)
  WHERE valid_to IS NULL;

-- Exact leading indexes for every foreign-key tuple keep parent lifecycle
-- operations bounded and make the complete schema contract auditable.
CREATE INDEX audit_logs_company_workspace_fk_idx ON audit_logs (company_id, organization_id) WHERE company_id IS NOT NULL;
CREATE INDEX oauth_states_reconnect_account_workspace_fk_idx ON oauth_states (reconnect_platform_account_id, organization_id) WHERE reconnect_platform_account_id IS NOT NULL;
CREATE INDEX organizations_deletion_receipt_fk_idx ON organizations (deletion_receipt_id) WHERE deletion_receipt_id IS NOT NULL;
CREATE INDEX platform_deletion_workspaces_organization_fk_idx ON platform_data_deletion_request_workspaces (organization_id);
CREATE INDEX content_items_company_workspace_fk_idx ON content_items (company_id, organization_id);
CREATE INDEX content_items_creator_company_workspace_fk_idx ON content_items (creator_id, company_id, organization_id);
CREATE INDEX content_items_created_by_fk_idx ON content_items (created_by);
CREATE INDEX content_revisions_organization_fk_idx ON content_revisions (organization_id);
CREATE INDEX content_revisions_created_by_fk_idx ON content_revisions (created_by);
CREATE INDEX content_publish_targets_revision_workspace_fk_idx ON content_publish_targets (content_revision_id, organization_id);
CREATE INDEX content_publish_targets_company_workspace_fk_idx ON content_publish_targets (company_id, organization_id);
CREATE INDEX content_publish_targets_creator_company_workspace_fk_idx ON content_publish_targets (creator_id, company_id, organization_id);
CREATE INDEX content_publish_targets_account_workspace_fk_idx ON content_publish_targets (platform_account_id, organization_id);
CREATE INDEX content_approval_revision_workspace_fk_idx ON content_approval_requests (content_revision_id, organization_id);
CREATE INDEX content_approval_requested_by_fk_idx ON content_approval_requests (requested_by);
CREATE INDEX content_approval_decided_by_fk_idx ON content_approval_requests (decided_by) WHERE decided_by IS NOT NULL;
CREATE INDEX content_publish_jobs_target_workspace_fk_idx ON content_publish_jobs (target_id, organization_id);
CREATE INDEX content_publish_jobs_revision_workspace_fk_idx ON content_publish_jobs (content_revision_id, organization_id);
CREATE INDEX content_publish_jobs_organization_fk_idx ON content_publish_jobs (organization_id);
CREATE INDEX content_publish_jobs_company_workspace_fk_idx ON content_publish_jobs (company_id, organization_id);
CREATE INDEX content_publish_attempts_target_workspace_fk_idx ON content_publish_attempts (target_id, organization_id);
CREATE INDEX content_publish_attempts_organization_fk_idx ON content_publish_attempts (organization_id);
CREATE INDEX content_notifications_organization_fk_idx ON content_notifications (organization_id);
CREATE INDEX content_notifications_company_workspace_fk_idx ON content_notifications (company_id, organization_id);
CREATE INDEX content_notifications_item_fk_idx ON content_notifications (content_item_id);
CREATE INDEX content_command_idempotency_actor_fk_idx ON content_command_idempotency (actor_id);
CREATE INDEX creator_content_policies_creator_workspace_fk_idx ON creator_content_approval_policies (creator_id, organization_id);
CREATE INDEX creator_content_policies_company_workspace_fk_idx ON creator_content_approval_policies (company_id, organization_id);
CREATE INDEX creator_content_policies_updated_by_fk_idx ON creator_content_approval_policies (updated_by) WHERE updated_by IS NOT NULL;
CREATE INDEX publications_content_item_fk_idx ON publications (content_item_id) WHERE content_item_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS publications_content_item_fk_idx;
DROP INDEX IF EXISTS creator_content_policies_updated_by_fk_idx;
DROP INDEX IF EXISTS creator_content_policies_company_workspace_fk_idx;
DROP INDEX IF EXISTS creator_content_policies_creator_workspace_fk_idx;
DROP INDEX IF EXISTS platform_deletion_workspaces_organization_fk_idx;
DROP INDEX IF EXISTS organizations_deletion_receipt_fk_idx;
DROP INDEX IF EXISTS audit_logs_company_workspace_fk_idx;
-- The pre-00025 schema requires both owners. Retained lifecycle tombstones can
-- legitimately have either FK cleared while 00025 is active, and their owners
-- cannot be reconstructed during rollback.
DELETE FROM publications WHERE creator_id IS NULL OR platform_account_id IS NULL;
ALTER TABLE publications
  DROP CONSTRAINT publications_creator_workspace_fkey,
  ADD CONSTRAINT publications_creator_workspace_fkey FOREIGN KEY (creator_id, organization_id) REFERENCES creators(id, organization_id) ON DELETE CASCADE,
  DROP CONSTRAINT publications_account_workspace_fkey,
  ADD CONSTRAINT publications_account_workspace_fkey FOREIGN KEY (platform_account_id, organization_id) REFERENCES platform_accounts(id, organization_id) ON DELETE CASCADE,
  ALTER COLUMN creator_id SET NOT NULL,
  ALTER COLUMN platform_account_id SET NOT NULL;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION validate_publication_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE creator_workspace uuid; creator_company uuid; account_workspace uuid; account_company uuid;
BEGIN
  IF TG_OP='UPDATE' AND (NEW.organization_id IS DISTINCT FROM OLD.organization_id OR NEW.creator_id IS DISTINCT FROM OLD.creator_id OR NEW.platform_account_id IS DISTINCT FROM OLD.platform_account_id) THEN
    RAISE EXCEPTION 'publication ownership cannot be changed' USING ERRCODE='23514';
  END IF;
  SELECT creator.organization_id,creator.company_id,account.organization_id,account.company_id INTO creator_workspace,creator_company,account_workspace,account_company
  FROM creators creator CROSS JOIN platform_accounts account WHERE creator.id=NEW.creator_id AND account.id=NEW.platform_account_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'publication creator or platform account does not exist' USING ERRCODE='23503'; END IF;
  IF NEW.organization_id<>creator_workspace OR NEW.organization_id<>account_workspace THEN RAISE EXCEPTION 'publication resources belong to another workspace' USING ERRCODE='23514'; END IF;
  IF creator_company<>account_company THEN RAISE EXCEPTION 'publication resources belong to different companies' USING ERRCODE='23514'; END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP INDEX IF EXISTS oauth_states_reconnect_generation_idx;
ALTER TABLE oauth_states
  DROP CONSTRAINT IF EXISTS oauth_states_reconnect_account_workspace_fkey,
  DROP CONSTRAINT IF EXISTS oauth_states_reconnect_generation_check,
  DROP COLUMN IF EXISTS authorization_generation,
  DROP COLUMN IF EXISTS reconnect_platform_account_id;
ALTER TABLE oauth_connections DROP COLUMN IF EXISTS authorization_generation;
ALTER TABLE publications DROP COLUMN IF EXISTS content_publish_target_id, DROP COLUMN IF EXISTS content_item_id;
DROP TRIGGER IF EXISTS content_publish_targets_validate_scope ON content_publish_targets;
DROP FUNCTION IF EXISTS validate_content_publish_target_scope();
DROP INDEX IF EXISTS creator_account_assignments_active_creator_account_scope_idx;
DROP TRIGGER IF EXISTS content_items_validate_scope ON content_items;
DROP FUNCTION IF EXISTS validate_content_item_scope();
DROP TABLE IF EXISTS content_command_idempotency, content_notifications, content_publish_attempts, content_publish_jobs, content_approval_requests, content_publish_targets, content_revisions, content_items CASCADE;
ALTER TABLE creators DROP CONSTRAINT IF EXISTS creators_id_company_workspace_key;
