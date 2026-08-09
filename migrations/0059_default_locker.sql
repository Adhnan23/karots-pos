-- +goose Up
-- Cashier default locker + the "allow untracked cash movements" policy.
--
-- Both default to today's behaviour: untracked movements stay allowed and there
-- is no default locker, so every till money-move (open / deposit / withdraw /
-- close) behaves exactly as before until an owner opts in on the Settings page.
--
-- default_locker_id is a soft pointer: ON DELETE SET NULL so removing the locker
-- simply clears the default rather than blocking the delete.
ALTER TABLE settings ADD COLUMN allow_untracked_cash BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE settings ADD COLUMN default_locker_id BIGINT REFERENCES lockers (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE settings DROP COLUMN default_locker_id;
ALTER TABLE settings DROP COLUMN allow_untracked_cash;
