-- +goose Up
-- Two independent permissions the shop had no way to express.
--
-- can_handle_suppliers marks a cashier trusted to pay suppliers, take in
-- deliveries and place orders at the till. It defaults to FALSE so no existing
-- cashier silently gains the ability — the owner opts each person in.
--
-- cashier_access marks a locker a cashier may move money into or out of. It
-- defaults to TRUE because that is exactly today's behaviour: the withdraw
-- dialog already lists every active locker. Switching it off on the owner's
-- safe is the new capability.
ALTER TABLE users   ADD COLUMN can_handle_suppliers boolean NOT NULL DEFAULT false;
ALTER TABLE lockers ADD COLUMN cashier_access       boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE users   DROP COLUMN can_handle_suppliers;
ALTER TABLE lockers DROP COLUMN cashier_access;
