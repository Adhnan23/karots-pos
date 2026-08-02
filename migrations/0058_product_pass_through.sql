-- +goose Up
-- pass_through marks a product whose sale is money passing through the shop, not
-- the shop's own margin (resold airtime, bill face value, gift cards). Core
-- reports exclude these lines from revenue, COGS and profit. Default false keeps
-- every existing product a normal sale.
ALTER TABLE products ADD COLUMN pass_through boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE products DROP COLUMN pass_through;
