-- +goose Up
-- System-user-locked appearance: a "skin" (brand identity, with light+dark
-- palettes), a density, and a receipt style. Defaults are the onboard seed.
-- Not editable from the admin Settings page — only the system-user panel writes
-- these, so the normal settings UPDATE deliberately leaves them untouched.
ALTER TABLE settings ADD COLUMN skin          TEXT NOT NULL DEFAULT 'default';
ALTER TABLE settings ADD COLUMN density       TEXT NOT NULL DEFAULT 'comfortable';
ALTER TABLE settings ADD COLUMN receipt_style TEXT NOT NULL DEFAULT 'classic';

-- +goose Down
ALTER TABLE settings DROP COLUMN receipt_style;
ALTER TABLE settings DROP COLUMN density;
ALTER TABLE settings DROP COLUMN skin;
