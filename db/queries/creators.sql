-- name: ListCreators :many
SELECT id, first_name, last_name, middle_name, display_name, status, created_at
FROM creators WHERE status <> 'ARCHIVED' ORDER BY display_name;

-- name: CreateCreator :one
INSERT INTO creators (first_name, last_name, middle_name, display_name, internal_note)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, first_name, last_name, middle_name, display_name, status, created_at;
