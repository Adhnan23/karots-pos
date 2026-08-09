-- +goose Up
ALTER TABLE doc_job ADD COLUMN kind TEXT NOT NULL DEFAULT 'sale';   -- sale | own_use
ALTER TABLE doc_job ADD COLUMN reversed_at TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE doc_job DROP COLUMN reversed_at;
ALTER TABLE doc_job DROP COLUMN kind;
