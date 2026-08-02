-- +goose Up
-- can_manage_credit marks a cashier trusted to override a customer's credit
-- limit for a single sale and to raise a customer's stored credit limit from the
-- till. Off by default; admins and managers may always do so regardless.
ALTER TABLE users ADD COLUMN can_manage_credit boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN can_manage_credit;
