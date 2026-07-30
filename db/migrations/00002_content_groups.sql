-- +goose Up
CREATE TABLE content_groups (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id),
  name text NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE',
  created_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE content_group_members (
  content_group_id uuid NOT NULL REFERENCES content_groups(id) ON DELETE CASCADE,
  publication_id uuid NOT NULL UNIQUE REFERENCES publications(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(content_group_id,publication_id)
);
CREATE TABLE content_match_suggestions (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  creator_id uuid NOT NULL REFERENCES creators(id),
  publication_a_id uuid NOT NULL REFERENCES publications(id),
  publication_b_id uuid NOT NULL REFERENCES publications(id),
  score numeric(5,2) NOT NULL CHECK(score BETWEEN 0 AND 100),
  reasons jsonb NOT NULL DEFAULT '[]'::jsonb,
  status text NOT NULL DEFAULT 'PENDING',
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  UNIQUE(publication_a_id,publication_b_id)
);
-- +goose Down
DROP TABLE IF EXISTS content_match_suggestions,content_group_members,content_groups;
