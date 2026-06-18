-- +goose NO TRANSACTION
-- +goose Up
-- Add a 'wallet' tender so a product sale can be paid by a mobile-wallet
-- transfer (eZ Cash / mCash). Additive on the core payment_method enum; the
-- plugin then attributes the received e-money to a carrier float via a wallet_in
-- transaction. ALTER TYPE ADD VALUE cannot run inside a transaction, hence the
-- NO TRANSACTION pragma above.
ALTER TYPE payment_method ADD VALUE IF NOT EXISTS 'wallet';

-- +goose Down
-- Postgres cannot drop an enum value; this is an intentional no-op so the Down
-- migration still succeeds.
SELECT 1;
