-- +goose Up
-- Credit stops being a sale type and becomes a payment method (the
-- payment_method enum already carries 'credit'). Every stored credit sale type
-- is rewritten to 'retail', the price list it always really was — sale_type
-- only ever affected pricing, and only for wholesale.
--
-- settings.default_sale_type is the one that matters. It seeds every sale at
-- the till, so leaving it as 'credit' while the API stops accepting that value
-- would reject every sale and strand the shop behind a setting only a working
-- till can reach.
UPDATE sales      SET sale_type = 'retail' WHERE sale_type = 'credit';
UPDATE held_sales SET sale_type = 'retail' WHERE sale_type = 'credit';
UPDATE settings   SET default_sale_type = 'retail' WHERE default_sale_type = 'credit';

-- +goose Down
-- Deliberately a no-op. Which rows were once 'credit' is not recoverable, and
-- inventing them would be worse than leaving them as the price list they are.
-- Re-accepting the value is a code concern, not a schema one: the enum label
-- was never dropped, so a rolled-back binary still writes and reads it.
SELECT 1;
