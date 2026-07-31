-- +goose Up
ALTER TABLE creators
  ADD COLUMN work_status text NOT NULL DEFAULT 'OK',
  ADD COLUMN work_comment text NOT NULL DEFAULT '',
  ADD CONSTRAINT creators_work_status_check CHECK (work_status IN ('OK', 'NEEDS_ATTENTION')),
  ADD CONSTRAINT creators_work_comment_check CHECK (work_status = 'OK' OR btrim(work_comment) <> '');

-- +goose Down
ALTER TABLE creators
  DROP CONSTRAINT IF EXISTS creators_work_comment_check,
  DROP CONSTRAINT IF EXISTS creators_work_status_check,
  DROP COLUMN IF EXISTS work_comment,
  DROP COLUMN IF EXISTS work_status;
