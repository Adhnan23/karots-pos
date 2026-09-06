-- +goose Up
CREATE TABLE aicatalog_settings (
  id             SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  provider       TEXT NOT NULL DEFAULT 'gemini',
  base_url       TEXT NOT NULL DEFAULT 'https://generativelanguage.googleapis.com/v1beta/openai',
  model          TEXT NOT NULL DEFAULT 'gemini-2.5-flash',
  api_key        TEXT NOT NULL DEFAULT '',
  default_markup NUMERIC(6,3) NOT NULL DEFAULT 1.400
);
INSERT INTO aicatalog_settings (id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE aicatalog_items (
  product_id         BIGINT PRIMARY KEY REFERENCES products(id) ON DELETE CASCADE,
  resolved_name      TEXT NOT NULL,
  suggested_category TEXT NOT NULL DEFAULT '',
  specs              TEXT NOT NULL DEFAULT '',
  user_explanation   TEXT NOT NULL DEFAULT '',
  source             TEXT NOT NULL,
  raw_query          TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE aicatalog_items;
DROP TABLE aicatalog_settings;
