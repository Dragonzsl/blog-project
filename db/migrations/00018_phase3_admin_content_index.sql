-- +goose Up

-- The admin content list always scopes by kind and trash state, then orders by
-- updated_at/id. Keep the bounded editorial page and its COUNT(*) from
-- scanning and sorting the whole contents table as the site grows.
CREATE INDEX contents_admin_list_idx
    ON contents(kind, trashed_at, updated_at DESC, id DESC);

-- +goose Down
DROP INDEX contents_admin_list_idx;
