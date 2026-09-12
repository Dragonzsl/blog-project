-- +goose Up

-- Feed output is a public presentation preference, not provider configuration.
-- Keep the value bounded so an invalid setting cannot change query behavior.
ALTER TABLE sites ADD COLUMN feed_summary_mode TEXT NOT NULL DEFAULT 'excerpt'
    CHECK (feed_summary_mode IN ('excerpt', 'full'));

-- +goose Down
ALTER TABLE sites DROP COLUMN feed_summary_mode;
