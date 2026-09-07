-- +goose Up
-- Mock (staging) products: fast bulk entry as drafts, optionally AI-enriched,
-- then committed into real products in one go (revertible). Nothing here touches
-- the real catalog until commit; a committed row keeps created_product_id so the
-- commit can be reversed.
CREATE TABLE aicatalog_mock_products (
    id                 BIGSERIAL PRIMARY KEY,
    name               TEXT NOT NULL,
    detail             TEXT NOT NULL DEFAULT '',
    qty                TEXT NOT NULL DEFAULT '',
    barcode            TEXT NOT NULL DEFAULT '',
    cost_price         TEXT NOT NULL DEFAULT '',
    selling_price      TEXT NOT NULL DEFAULT '',
    category           TEXT NOT NULL DEFAULT '',
    specs              TEXT NOT NULL DEFAULT '',
    explanation        TEXT NOT NULL DEFAULT '',
    confident          BOOLEAN NOT NULL DEFAULT false,
    status             TEXT NOT NULL DEFAULT 'draft',   -- draft | committed
    created_product_id BIGINT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE aicatalog_mock_products;
