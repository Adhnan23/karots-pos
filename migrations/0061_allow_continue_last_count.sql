-- +goose Up
-- Whether the till's Open Register dialog offers "Continue last" (pre-fill the
-- opening count from this cashier's own last close). In a single-drawer shop
-- shared by more than one person, that carried-forward figure goes stale the
-- moment someone else uses the drawer between sessions, so the owner can turn it
-- off and force a fresh count every open. Default true = current behaviour.
ALTER TABLE settings ADD COLUMN allow_continue_last_count BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE settings DROP COLUMN allow_continue_last_count;
