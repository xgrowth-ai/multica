CREATE TABLE design_draft (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    entry_path text NOT NULL,
    storage_revision uuid NOT NULL,
    manifest jsonb NOT NULL DEFAULT '[]'::jsonb,
    total_size bigint NOT NULL CHECK (total_size >= 0),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
