-- +goose Up
-- One level of subcategories: a category may sit under a top-level category
-- of the same kind (the API enforces the depth and the kind).
ALTER TABLE categories ADD COLUMN parent_id uuid CHECK (parent_id <> id);
ALTER TABLE categories ADD CONSTRAINT categories_parent_fk
    FOREIGN KEY (workspace_id, parent_id) REFERENCES categories (workspace_id, id);
CREATE INDEX categories_parent_idx ON categories (parent_id) WHERE parent_id IS NOT NULL;

-- Names are unique among siblings, so "Otros" can sit under several parents.
DROP INDEX categories_unique_active_name;
CREATE UNIQUE INDEX categories_unique_active_name
    ON categories (workspace_id, kind, coalesce(parent_id, '00000000-0000-0000-0000-000000000000'), lower(name))
    WHERE archived_at IS NULL;

-- +goose Down
DROP INDEX categories_unique_active_name;
CREATE UNIQUE INDEX categories_unique_active_name
    ON categories (workspace_id, kind, lower(name)) WHERE archived_at IS NULL;
DROP INDEX categories_parent_idx;
ALTER TABLE categories DROP CONSTRAINT categories_parent_fk;
ALTER TABLE categories DROP COLUMN parent_id;
