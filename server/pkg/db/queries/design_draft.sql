-- name: ListDesignDrafts :many
SELECT * FROM design_draft
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: GetDesignDraftInWorkspace :one
SELECT * FROM design_draft
WHERE id = $1 AND workspace_id = $2;

-- name: GetDesignDraft :one
SELECT * FROM design_draft
WHERE id = $1;

-- name: CreateDesignDraft :one
INSERT INTO design_draft (
    id, workspace_id, name, entry_path, storage_revision, manifest,
    total_size, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: RenameDesignDraft :one
UPDATE design_draft
SET name = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteDesignDraft :execrows
DELETE FROM design_draft WHERE id = $1 AND workspace_id = $2;

-- name: DeleteDesignDraftsByWorkspace :execrows
DELETE FROM design_draft WHERE workspace_id = $1;
