-- +goose Up
-- Manual "bring your own chatbot" mode: no API key; the plugin hands the owner a
-- prompt to paste into any chatbot (with free web search) and reads the reply back.
ALTER TABLE aicatalog_settings ADD COLUMN manual_mode BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE aicatalog_settings DROP COLUMN manual_mode;
