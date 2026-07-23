-- +goose Up
-- A stable, per-install identifier.
--
-- It exists so the support (system) account can carry a DIFFERENT PIN in every
-- shop instead of one secret compiled into every binary: the PIN is derived from
-- a developer master secret and this id, so the owner reads the id off their
-- screen and the developer derives that shop's PIN. One shop's credential
-- leaking then tells you nothing about any other shop.
--
-- Generated once here rather than at boot so it survives restarts and appears in
-- backups (the archive discovers columns dynamically, so it rides along).
ALTER TABLE settings ADD COLUMN install_id TEXT NOT NULL DEFAULT '';

UPDATE settings
   SET install_id = upper(substr(md5(random()::text || clock_timestamp()::text), 1, 8))
 WHERE install_id = '';

-- +goose Down
ALTER TABLE settings DROP COLUMN install_id;
