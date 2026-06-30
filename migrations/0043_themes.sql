-- +goose Up
CREATE TABLE themes (
    id         SERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    palette    TEXT NOT NULL DEFAULT 'classic',
    mode       TEXT NOT NULL DEFAULT 'auto',          -- light | dark | auto
    density    TEXT NOT NULL DEFAULT 'comfortable',   -- comfortable | compact | large_touch
    accent     TEXT,                                  -- optional custom hex override (e.g. #4f46e5)
    is_builtin BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO themes (name, palette, mode, density, is_builtin) VALUES
    ('Classic', 'classic', 'auto', 'comfortable', true),
    ('Emerald', 'emerald', 'auto', 'comfortable', true),
    ('Ocean',   'ocean',   'auto', 'comfortable', true),
    ('Sunset',  'sunset',  'auto', 'comfortable', true);

ALTER TABLE settings ADD COLUMN active_theme_id INTEGER REFERENCES themes(id);
UPDATE settings SET active_theme_id = (SELECT id FROM themes WHERE palette = 'classic' LIMIT 1) WHERE id = 1;

-- +goose Down
ALTER TABLE settings DROP COLUMN IF EXISTS active_theme_id;
DROP TABLE IF EXISTS themes;
