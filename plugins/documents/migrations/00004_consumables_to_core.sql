-- +goose Up
-- doc_consumable was a per-service bill of materials that only the documents
-- plugin could read. Core now owns recipes, so the rows move across and the
-- plugin reads them from there.
--
-- Size-specific rows cannot move: core recipes key on the product alone, and
-- the paper a job consumes depends on the size chosen at the till. Those stay
-- in doc_consumable and the plugin keeps resolving them. Only size-agnostic
-- rows (NULL size) — toner, ink, anything used regardless of paper size — are
-- the ones core can own.
INSERT INTO product_recipes (product_id, component_product_id, qty_per_unit, whole_units)
SELECT s.product_id, dc.product_id, dc.qty_per_unit, true
FROM doc_consumable dc
JOIN doc_service s ON s.id = dc.service_id
WHERE dc.size IS NULL
ON CONFLICT (product_id, component_product_id) DO NOTHING;

-- +goose Down
-- Only remove what this migration could have inserted.
DELETE FROM product_recipes pr
USING doc_consumable dc, doc_service s
WHERE s.id = dc.service_id
  AND dc.size IS NULL
  AND pr.product_id = s.product_id
  AND pr.component_product_id = dc.product_id;
