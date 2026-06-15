-- +goose Up
-- Quick-added items (sold at the till before they were in the catalog) are flagged
-- for the admin to finish: set the real category, unit and cost. created_by records
-- which user rang up the quick-add, so the review queue shows who did it.
ALTER TABLE products ADD COLUMN needs_review BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE products ADD COLUMN created_by  BIGINT NULL REFERENCES users(id);

-- +goose Down
ALTER TABLE products DROP COLUMN IF EXISTS created_by;
ALTER TABLE products DROP COLUMN IF EXISTS needs_review;
