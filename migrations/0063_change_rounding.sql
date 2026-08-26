-- +goose Up
-- rounding_to_account records the portion of a sale's surplus (cash the customer
-- left behind instead of taking as change) that was booked to an account
-- customer's balance as an advance, rather than kept as a rounding gain in the
-- drawer. It exists only so a reprinted receipt and the customer's record still
-- tell the truth about what went on account — change_given alone (which now
-- always holds the ACTUAL change handed back, so the drawer reconciles) cannot
-- distinguish "kept as cash" from "put on account". Default 0 keeps every
-- existing sale a plain, exact-change sale.
ALTER TABLE sales ADD COLUMN rounding_to_account numeric NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sales DROP COLUMN rounding_to_account;
