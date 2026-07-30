-- +goose Up
CREATE TYPE user_role AS ENUM ('ADMIN', 'ANALYST', 'VIEWER');
CREATE TYPE user_status AS ENUM ('INVITED', 'ACTIVE', 'SUSPENDED');
CREATE TYPE creator_status AS ENUM ('ACTIVE', 'ARCHIVED');
CREATE TYPE platform AS ENUM ('INSTAGRAM', 'YOUTUBE', 'TIKTOK', 'VK');
CREATE TYPE account_status AS ENUM ('ACTIVE', 'PAUSED', 'REAUTH_REQUIRED', 'DISCONNECTED');

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  email text NOT NULL UNIQUE CHECK (email = lower(email)),
  password_hash text NOT NULL,
  role user_role NOT NULL DEFAULT 'VIEWER',
  status user_status NOT NULL DEFAULT 'ACTIVE',
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  ip inet,
  user_agent text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_active_idx ON sessions (token_hash, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE creators (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  first_name text NOT NULL,
  last_name text NOT NULL,
  middle_name text,
  display_name text NOT NULL,
  internal_note text NOT NULL DEFAULT '',
  status creator_status NOT NULL DEFAULT 'ACTIVE',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);

CREATE TABLE creator_contacts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id),
  kind text NOT NULL,
  value text NOT NULL,
  label text,
  is_primary boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform_accounts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  platform platform NOT NULL,
  external_id text NOT NULL,
  username text NOT NULL,
  display_name text NOT NULL,
  profile_url text,
  avatar_url text,
  account_type text,
  status account_status NOT NULL DEFAULT 'REAUTH_REQUIRED',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_synced_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (platform, external_id)
);

CREATE TABLE creator_account_assignments (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id),
  platform_account_id uuid NOT NULL REFERENCES platform_accounts(id),
  valid_from timestamptz NOT NULL DEFAULT now(),
  valid_to timestamptz,
  assigned_by uuid REFERENCES users(id),
  CHECK (valid_to IS NULL OR valid_to > valid_from)
);
CREATE UNIQUE INDEX account_one_active_creator_idx ON creator_account_assignments(platform_account_id) WHERE valid_to IS NULL;

CREATE TABLE publications (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id),
  platform_account_id uuid NOT NULL REFERENCES platform_accounts(id),
  platform platform NOT NULL,
  external_id text NOT NULL,
  publication_type text NOT NULL,
  title text,
  description text,
  permalink text,
  thumbnail_url text,
  duration_ms bigint,
  published_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  discovered_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (platform, external_id)
);
CREATE INDEX publications_creator_published_idx ON publications (creator_id, published_at DESC);

CREATE TABLE publication_metric_snapshots (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  publication_id uuid NOT NULL REFERENCES publications(id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  views bigint,
  reach bigint,
  likes bigint,
  comments bigint,
  shares bigint,
  saves bigint,
  watch_time_ms bigint,
  completeness_status text NOT NULL DEFAULT 'PARTIAL',
  UNIQUE (publication_id, observed_at)
);
CREATE INDEX publication_snapshots_observed_idx ON publication_metric_snapshots (publication_id, observed_at DESC);

CREATE TABLE sync_targets (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  target_type text NOT NULL,
  target_id uuid NOT NULL,
  operation text NOT NULL,
  cadence interval NOT NULL DEFAULT interval '6 hours',
  next_sync_at timestamptz NOT NULL DEFAULT now(),
  last_success_at timestamptz,
  last_error text,
  consecutive_failures integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'ACTIVE'
);

CREATE TABLE sync_runs (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  target_id uuid REFERENCES sync_targets(id),
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  outcome text NOT NULL DEFAULT 'RUNNING',
  records_read integer NOT NULL DEFAULT 0,
  records_written integer NOT NULL DEFAULT 0,
  error_message text
);

CREATE TABLE audit_logs (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  actor_id uuid REFERENCES users(id),
  action text NOT NULL,
  entity_type text NOT NULL,
  entity_id uuid,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS audit_logs, sync_runs, sync_targets, publication_metric_snapshots, publications,
  creator_account_assignments, platform_accounts, creator_contacts, creators, sessions, users CASCADE;
DROP TYPE IF EXISTS account_status, platform, creator_status, user_status, user_role;
