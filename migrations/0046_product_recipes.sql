-- +goose Up
-- A recipe is what one unit of a SERVICE consumes: a photocopy eats a sheet and
-- a slice of toner; a coffee eats powder, a cup and water.
--
-- Two ways to state a quantity, because shops think in both:
--   qty_per_unit — "18 grams of powder per cup"
--   yield_units  — "this bag makes 50 cups"  (consumed = qty / yield_units)
-- Exactly one is set. Yield is stored as the yield, never as its reciprocal:
-- 1/3000 written into a fixed-scale column loses precision, and the number the
-- owner actually knows is "3000", not "0.000333".
--
-- whole_units marks a component that cannot be part-used. A sheet of paper is
-- whole (a 1-copy job consumes 1 sheet, never 0.5); grams of coffee are not.
-- The documents plugin used to Ceil() EVERY component, which is right for paper
-- and catastrophic for a yield-based one — a single copy consumed a whole toner.
CREATE TABLE product_recipes (
	id                   BIGSERIAL PRIMARY KEY,
	product_id           BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	component_product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
	qty_per_unit         NUMERIC(14,6),
	yield_units          NUMERIC(14,4),
	whole_units          BOOLEAN NOT NULL DEFAULT false,
	note                 TEXT,
	created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

	-- Exactly one way of stating the quantity, both strictly positive.
	CONSTRAINT product_recipes_qty_xor_yield CHECK (
		(qty_per_unit IS NOT NULL AND qty_per_unit > 0 AND yield_units IS NULL)
		OR
		(yield_units IS NOT NULL AND yield_units > 0 AND qty_per_unit IS NULL)
	),
	-- A product consuming itself would deplete stock it does not have.
	CONSTRAINT product_recipes_no_self CHECK (product_id <> component_product_id),
	-- One row per (service, component): two rows for the same ingredient would
	-- double-consume it, which is how the old size-specific/NULL-size fallback
	-- could go wrong.
	CONSTRAINT product_recipes_unique_component UNIQUE (product_id, component_product_id)
);

CREATE INDEX idx_product_recipes_product ON product_recipes(product_id);

-- +goose Down
DROP TABLE product_recipes;
