-- +goose Up

-- logo_data holds an uploaded logo as a self-contained data: URI (base64 PNG),
-- so the logo works fully offline — no external URL fetch needed at print time.
-- It takes precedence over logo_url when set.
ALTER TABLE settings ADD COLUMN logo_data TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE settings DROP COLUMN logo_data;
