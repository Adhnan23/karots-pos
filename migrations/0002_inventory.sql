-- +goose Up

-- products: master catalog.
CREATE TABLE products (
  id              BIGSERIAL     PRIMARY KEY,
  name            VARCHAR(150)  NOT NULL,
  name_si         VARCHAR(150),
  barcode         VARCHAR(50)   UNIQUE,
  category_id     BIGINT        NOT NULL REFERENCES categories (id) ON DELETE RESTRICT,
  unit_id         BIGINT        NOT NULL REFERENCES units (id)       ON DELETE RESTRICT,
  cost_price      DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  selling_price   DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  wholesale_price DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  tax_rate        DECIMAL(5,2)  NOT NULL DEFAULT 0.00,
  reorder_level   INT           NOT NULL DEFAULT 0,
  is_active       BOOLEAN       NOT NULL DEFAULT true,
  created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_products_category_id ON products (category_id);
CREATE INDEX idx_products_barcode     ON products (barcode);
CREATE INDEX idx_products_is_active   ON products (is_active);
CREATE INDEX idx_products_name_search ON products USING gin (to_tsvector('simple', name));

-- stock: real-time level, exactly one row per product (maintained by trigger).
CREATE TABLE stock (
  id           BIGSERIAL     PRIMARY KEY,
  product_id   BIGINT        NOT NULL UNIQUE REFERENCES products (id) ON DELETE CASCADE,
  quantity     DECIMAL(12,3) NOT NULL DEFAULT 0.000,
  last_updated TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION create_stock_for_product()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO stock (product_id, quantity) VALUES (NEW.id, 0);
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_product_stock
  AFTER INSERT ON products
  FOR EACH ROW EXECUTE FUNCTION create_stock_for_product();

-- stock_movements: full audit trail of every quantity delta.
-- +goose StatementBegin
CREATE TYPE stock_movement_type AS ENUM ('purchase', 'sale', 'adjust', 'return', 'damage');
-- +goose StatementEnd

CREATE TABLE stock_movements (
  id             BIGSERIAL           PRIMARY KEY,
  product_id     BIGINT              NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
  type           stock_movement_type NOT NULL,
  quantity       DECIMAL(12,3)       NOT NULL,
  reference_id   BIGINT,
  reference_type VARCHAR(20),
  user_id        BIGINT              NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  note           TEXT,
  created_at     TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_stock_movements_product_id ON stock_movements (product_id);
CREATE INDEX idx_stock_movements_type       ON stock_movements (type);
CREATE INDEX idx_stock_movements_ref        ON stock_movements (reference_type, reference_id);
CREATE INDEX idx_stock_movements_created_at ON stock_movements (created_at DESC);

-- +goose Down
DROP TRIGGER IF EXISTS trg_product_stock ON products;
DROP FUNCTION IF EXISTS create_stock_for_product();
DROP TABLE stock_movements;
DROP TABLE stock;
DROP TABLE products;
DROP TYPE stock_movement_type;
