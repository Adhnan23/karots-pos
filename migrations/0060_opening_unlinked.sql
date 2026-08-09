-- +goose Up
-- Splits the onboarding "opening balance" (pre-system debt) into two figures so an
-- admin can correct it while the transactional part stays locked:
--   opening_balance  - the gross old debt (drives the customer ledger's opening
--                      line). Changed only by an admin edit, never by sales/payments.
--   opening_unlinked - how much of that old debt is still UNPAID: the editable
--                      figure. A payment settles the transactional (linked) part
--                      first, and any overflow erodes this. Clamped at the single
--                      balance chokepoint (AddBalance) so it can never exceed the
--                      current outstanding balance.
-- linked (transactional) is derived, never stored: outstanding_balance - opening_unlinked.
ALTER TABLE customers ADD COLUMN opening_unlinked NUMERIC(12, 2) NOT NULL DEFAULT 0;
ALTER TABLE suppliers ADD COLUMN opening_unlinked NUMERIC(12, 2) NOT NULL DEFAULT 0;

-- Backfill: best estimate of the still-unpaid opening for existing rows. Capped at
-- the current outstanding (payments may already have eroded it) and floored at 0
-- (a net advance/credit leaves nothing unlinked). Day-one behaviour is unchanged.
UPDATE customers SET opening_unlinked = LEAST(opening_balance, GREATEST(outstanding_balance, 0));
UPDATE suppliers SET opening_unlinked = LEAST(opening_balance, GREATEST(outstanding_balance, 0));

-- +goose Down
ALTER TABLE suppliers DROP COLUMN opening_unlinked;
ALTER TABLE customers DROP COLUMN opening_unlinked;
