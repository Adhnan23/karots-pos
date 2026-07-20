-- +goose Up
-- Opening float balance for a device.
--
-- When a shop is onboarded, its SIMs already hold money — that balance existed
-- before the POS did. There was no way to state it: float could only arrive via
-- a reload (books an expense) or a supplier refill (books an expense and moves
-- cash), both of which would invent a purchase that never happened. So every
-- device started at zero and read as overdrawn until someone refilled it.
--
-- 'opening' is the float equivalent of opening stock: it declares what is
-- already there. float_delta = +amount, cash_delta = 0, no expense, no drawer
-- movement. Recorded with session_id = 0 so DeviceBalance picks it up through
-- the same carry-over path that handles float moved outside a session.
ALTER TABLE recharge_transactions DROP CONSTRAINT IF EXISTS recharge_transactions_type_check;

ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
	CHECK (type = ANY (ARRAY[
		'deposit'::text, 'withdrawal'::text, 'billpay'::text, 'topup'::text,
		'wallet_in'::text, 'reload'::text, 'refill'::text, 'opening'::text
	]));

-- +goose Down
-- Rows of the new type must go before the old constraint can be re-imposed, or
-- the ALTER fails and the rollback leaves the table without any type check.
DELETE FROM recharge_transactions WHERE type = 'opening';

ALTER TABLE recharge_transactions DROP CONSTRAINT IF EXISTS recharge_transactions_type_check;

ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
	CHECK (type = ANY (ARRAY[
		'deposit'::text, 'withdrawal'::text, 'billpay'::text, 'topup'::text,
		'wallet_in'::text, 'reload'::text, 'refill'::text
	]));
