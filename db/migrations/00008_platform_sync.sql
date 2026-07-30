-- +goose Up
CREATE TABLE account_metric_snapshots (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  platform_account_id uuid NOT NULL REFERENCES platform_accounts(id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  followers bigint,
  follows bigint,
  media_count bigint,
  views bigint,
  reach bigint,
  interactions bigint,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX account_metric_snapshots_account_observed_idx
  ON account_metric_snapshots (platform_account_id, observed_at DESC);

CREATE TABLE account_daily_metrics (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  platform_account_id uuid NOT NULL REFERENCES platform_accounts(id) ON DELETE CASCADE,
  metric_date date NOT NULL,
  views bigint,
  reach bigint,
  likes bigint,
  comments bigint,
  shares bigint,
  interactions bigint,
  watch_time_ms bigint,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (platform_account_id, metric_date)
);
CREATE INDEX account_daily_metrics_account_date_idx
  ON account_daily_metrics (platform_account_id, metric_date DESC);

CREATE TABLE publication_daily_metrics (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  publication_id uuid NOT NULL REFERENCES publications(id) ON DELETE CASCADE,
  metric_date date NOT NULL,
  views bigint,
  reach bigint,
  likes bigint,
  comments bigint,
  shares bigint,
  saves bigint,
  watch_time_ms bigint,
  average_watch_time_ms bigint,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (publication_id, metric_date)
);
CREATE INDEX publication_daily_metrics_publication_date_idx
  ON publication_daily_metrics (publication_id, metric_date DESC);

CREATE TABLE platform_data_deletion_requests (
  confirmation_code text PRIMARY KEY,
  organization_id uuid REFERENCES organizations(id) ON DELETE SET NULL,
  platform_account_id uuid REFERENCES platform_accounts(id) ON DELETE SET NULL,
  platform platform NOT NULL,
  external_id text NOT NULL,
  status text NOT NULL DEFAULT 'PENDING',
  requested_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  error_message text
);
CREATE INDEX platform_data_deletion_requests_account_idx
  ON platform_data_deletion_requests (platform, external_id, requested_at DESC);

-- +goose Down
DROP TABLE IF EXISTS platform_data_deletion_requests;
DROP TABLE IF EXISTS publication_daily_metrics;
DROP TABLE IF EXISTS account_daily_metrics;
DROP TABLE IF EXISTS account_metric_snapshots;
