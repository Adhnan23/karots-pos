-- +goose Up
-- Carrier names must be unique among ACTIVE carriers only.
--
-- The old constraint was a plain UNIQUE on name, but deleting a carrier is a
-- SOFT delete (is_active = false) that keeps the row — and therefore keeps the
-- name — forever. Re-adding a carrier you had deleted was impossible: the app's
-- own guard (CarrierExists) only looked at active names, said "fine", and then
-- the database rejected the insert. The visible damage in the field was a shop
-- that deleted "Dialog", could not re-add it, and settled for "Dialogs".
--
-- Carriers are no longer deleted at all. They are DISABLED (hidden from the
-- till and the pickers) and can be re-enabled, and a carrier that rebrands is
-- renamed rather than replaced. Disabled rows keep their names, so uniqueness
-- has to be scoped to the active ones or a disabled carrier would hold its name
-- hostage against a rename.
--
-- Case-insensitive, because "dialog" and "Dialog" are the same carrier to a
-- human and CarrierExists has always compared with lower().
ALTER TABLE recharge_carriers DROP CONSTRAINT IF EXISTS recharge_carriers_name_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_recharge_carriers_active_name
	ON recharge_carriers (lower(name)) WHERE is_active;

-- +goose Down
DROP INDEX IF EXISTS idx_recharge_carriers_active_name;
-- The plain UNIQUE is intentionally NOT restored: re-adding it would fail on any
-- database that has since taken advantage of the looser rule (two carriers
-- sharing a name where one is retired), turning a rollback into an outage.
