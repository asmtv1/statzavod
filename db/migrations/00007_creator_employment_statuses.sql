-- +goose Up
ALTER TYPE creator_status ADD VALUE IF NOT EXISTS 'ON_LEAVE';
ALTER TYPE creator_status ADD VALUE IF NOT EXISTS 'DISMISSED';

-- +goose Down
-- PostgreSQL enum values are intentionally retained on rollback.
