-- +goose Up
-- Cost lines are the part of a recipe that is NOT stock: electricity for the
-- coffee machine, a service fee, gas. They exist so the owner can see what a cup
-- really costs before setting its price.
--
-- They deliberately do NOT reach the sale transaction or the P&L. The real
-- electricity bill is already an expense (tagged to the service via
-- expenses.service_product_id); adding an estimated per-cup figure to COGS as
-- well would charge the shop for the same electricity twice.
CREATE TABLE product_recipe_costs (
	id BIGSERIAL PRIMARY KEY,
	product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	label TEXT NOT NULL,
	cost_per_unit NUMERIC(14,4) NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT product_recipe_costs_positive CHECK (cost_per_unit >= 0),
	CONSTRAINT product_recipe_costs_label_set CHECK (btrim(label) <> ''),
	CONSTRAINT product_recipe_costs_unique_label UNIQUE (product_id, label)
);

CREATE INDEX idx_product_recipe_costs_product ON product_recipe_costs (product_id);

-- +goose Down
DROP TABLE product_recipe_costs;
