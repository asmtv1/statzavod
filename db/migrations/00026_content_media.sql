-- +goose Up
CREATE TABLE media_assets (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid NOT NULL REFERENCES organizations(id),
  company_id uuid NOT NULL,
  creator_id uuid NOT NULL,
  kind text NOT NULL DEFAULT 'VIDEO' CHECK (kind='VIDEO'),
  status text NOT NULL DEFAULT 'UPLOADING' CHECK (status IN ('UPLOADING','VALIDATING','READY','REJECTED','DELETE_PENDING','DELETED')),
  object_key text,
  content_sha256 bytea,
  original_filename text NOT NULL,
  detected_mime text,
  bytes bigint CHECK (bytes >= 0),
  width integer CHECK (width > 0),
  height integer CHECK (height > 0),
  duration_ms bigint CHECK (duration_ms >= 0),
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  rejected_reason text,
  created_at timestamptz NOT NULL DEFAULT now(),
  ready_at timestamptz,
  delete_after timestamptz,
  validation_worker_id text,
  validation_lease_until timestamptz,
  validation_generation bigint NOT NULL DEFAULT 0 CHECK (validation_generation >= 0),
  cleanup_worker_id text,
  cleanup_lease_until timestamptz,
  cleanup_generation bigint NOT NULL DEFAULT 0 CHECK (cleanup_generation >= 0),
  UNIQUE (id, organization_id),
  UNIQUE (id, company_id, creator_id, organization_id),
  FOREIGN KEY (company_id, organization_id) REFERENCES companies(id, organization_id),
  FOREIGN KEY (creator_id, company_id, organization_id) REFERENCES creators(id, company_id, organization_id),
  CHECK ((status='READY' AND object_key IS NOT NULL AND content_sha256 IS NOT NULL AND height > width) OR status <> 'READY')
);
CREATE INDEX media_assets_gc_idx ON media_assets (delete_after, id) WHERE status IN ('REJECTED','DELETE_PENDING');
CREATE INDEX media_assets_validation_idx ON media_assets (created_at, id) WHERE status='VALIDATING';
CREATE INDEX media_assets_content_sha256_idx ON media_assets (organization_id, content_sha256) WHERE content_sha256 IS NOT NULL;
ALTER TABLE content_publish_targets ADD CONSTRAINT content_publish_targets_media_scope_fkey FOREIGN KEY (media_asset_id, company_id, creator_id, organization_id) REFERENCES media_assets(id, company_id, creator_id, organization_id);
CREATE INDEX content_publish_targets_media_asset_idx ON content_publish_targets (media_asset_id, company_id, creator_id, organization_id) WHERE media_asset_id IS NOT NULL;

-- Media arrives after the publishing schema, so extend the existing target
-- trigger here with the creator/company media ownership check.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION validate_content_publish_target_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE revision_item uuid; item_company uuid; item_creator uuid; account_platform platform; account_company uuid;
BEGIN
  IF NEW.media_asset_id IS NOT NULL THEN
    PERFORM pg_advisory_xact_lock(hashtextextended('media-asset:'||NEW.media_asset_id::text,0));
  END IF;
  SELECT content_item_id INTO revision_item FROM content_revisions WHERE id=NEW.content_revision_id AND organization_id=NEW.organization_id;
  SELECT company_id,creator_id INTO item_company,item_creator FROM content_items WHERE id=revision_item AND organization_id=NEW.organization_id;
  SELECT platform,company_id INTO account_platform,account_company FROM platform_accounts WHERE id=NEW.platform_account_id AND organization_id=NEW.organization_id;
  IF revision_item IS NULL OR item_company <> NEW.company_id OR item_creator <> NEW.creator_id OR account_platform <> NEW.platform OR account_company <> NEW.company_id
     OR (NEW.media_asset_id IS NOT NULL AND NOT EXISTS (
       SELECT 1 FROM media_assets media
       WHERE media.id=NEW.media_asset_id AND media.organization_id=NEW.organization_id
         AND media.company_id=NEW.company_id AND media.creator_id=NEW.creator_id
         AND media.status='READY'
     ))
     OR NOT (
       EXISTS (
         SELECT 1 FROM creator_account_assignments assignment
         WHERE assignment.creator_id=NEW.creator_id AND assignment.platform_account_id=NEW.platform_account_id
           AND assignment.organization_id=NEW.organization_id AND assignment.valid_to IS NULL
       )
       OR (NEW.platform='VK' AND EXISTS (
         SELECT 1 FROM company_vk_accounts vk
         WHERE vk.platform_account_id=NEW.platform_account_id AND vk.organization_id=NEW.organization_id AND vk.company_id=NEW.company_id
       ))
     ) THEN
    RAISE EXCEPTION 'content target resources cross tenant, company, creator, media, or platform' USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE media_upload_sessions (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  media_asset_id uuid NOT NULL,
  organization_id uuid NOT NULL REFERENCES organizations(id),
  actor_id uuid NOT NULL REFERENCES users(id),
  multipart_upload_id text NOT NULL,
  expected_bytes bigint NOT NULL CHECK (expected_bytes > 0),
  expected_mime text NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','COMPLETED','ABORTED','EXPIRED')),
  expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
  completed_at timestamptz,
  cleanup_worker_id text,
  cleanup_lease_until timestamptz,
  cleanup_generation bigint NOT NULL DEFAULT 0 CHECK (cleanup_generation >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, organization_id),
  FOREIGN KEY (media_asset_id, organization_id) REFERENCES media_assets(id, organization_id)
);
CREATE INDEX media_upload_sessions_gc_idx ON media_upload_sessions (expires_at, id) WHERE status='ACTIVE';
CREATE INDEX media_upload_sessions_org_quota_idx ON media_upload_sessions (organization_id, created_at, id) WHERE status='ACTIVE';
CREATE INDEX media_upload_sessions_actor_quota_idx ON media_upload_sessions (organization_id, actor_id, created_at, id) WHERE status='ACTIVE';
CREATE INDEX media_upload_sessions_media_asset_idx ON media_upload_sessions (media_asset_id, organization_id);
CREATE INDEX media_upload_sessions_actor_idx ON media_upload_sessions (actor_id);
CREATE INDEX media_assets_company_workspace_idx ON media_assets (company_id, organization_id);
CREATE INDEX media_assets_creator_company_workspace_idx ON media_assets (creator_id, company_id, organization_id);

-- Remote cleanup must outlive company/creator rows.  This deliberately has no
-- tenant/domain foreign keys: lifecycle purge first records the exact remote
-- operation here, then may remove the company graph in the same transaction.
-- A worker deletes this durable receipt only after S3 confirms the desired
-- state; expired leases make an uncertain/crashed attempt safe to retry.
CREATE TABLE media_cleanup_tasks (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  task_key text NOT NULL UNIQUE,
  organization_id uuid NOT NULL,
  former_company_id uuid,
  former_creator_id uuid,
  former_media_asset_id uuid,
  kind text NOT NULL CHECK (kind IN ('ABORT_MULTIPART','DELETE_OBJECT')),
  object_key text NOT NULL,
  multipart_upload_id text,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','COMPLETE')),
  lease_worker_id text,
  lease_until timestamptz,
  generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CHECK ((kind='ABORT_MULTIPART' AND multipart_upload_id IS NOT NULL) OR (kind='DELETE_OBJECT' AND multipart_upload_id IS NULL))
);
CREATE INDEX media_cleanup_tasks_claim_idx ON media_cleanup_tasks (created_at, id) WHERE status IN ('PENDING','RUNNING');

-- +goose Down
DROP TABLE IF EXISTS media_cleanup_tasks;
DROP TABLE IF EXISTS media_upload_sessions;
DROP INDEX IF EXISTS content_publish_targets_media_asset_idx;
ALTER TABLE content_publish_targets DROP CONSTRAINT IF EXISTS content_publish_targets_media_scope_fkey;
-- media_assets is introduced by this migration. A rollback cannot preserve a
-- target reference to a table that is about to be removed, so clear only this
-- v26 relationship before restoring the v25 validator.
UPDATE content_publish_targets SET media_asset_id=NULL WHERE media_asset_id IS NOT NULL;
-- Restore the v25 target validator before removing media_assets. Leaving the
-- replacement body in place would make a down migration reference a table
-- that no longer exists.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION validate_content_publish_target_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE revision_item uuid; item_company uuid; item_creator uuid; account_platform platform; account_company uuid;
BEGIN
  SELECT content_item_id INTO revision_item FROM content_revisions WHERE id=NEW.content_revision_id AND organization_id=NEW.organization_id;
  SELECT company_id,creator_id INTO item_company,item_creator FROM content_items WHERE id=revision_item AND organization_id=NEW.organization_id;
  SELECT platform,company_id INTO account_platform,account_company FROM platform_accounts WHERE id=NEW.platform_account_id AND organization_id=NEW.organization_id;
  IF revision_item IS NULL OR item_company <> NEW.company_id OR item_creator <> NEW.creator_id OR account_platform <> NEW.platform OR account_company <> NEW.company_id
     OR NOT (
       EXISTS (SELECT 1 FROM creator_account_assignments assignment WHERE assignment.creator_id=NEW.creator_id AND assignment.platform_account_id=NEW.platform_account_id AND assignment.organization_id=NEW.organization_id AND assignment.valid_to IS NULL)
       OR (NEW.platform='VK' AND EXISTS (SELECT 1 FROM company_vk_accounts vk WHERE vk.platform_account_id=NEW.platform_account_id AND vk.organization_id=NEW.organization_id AND vk.company_id=NEW.company_id))
     ) THEN
    RAISE EXCEPTION 'content target resources cross tenant, company, creator, or platform' USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TABLE IF EXISTS media_assets;
