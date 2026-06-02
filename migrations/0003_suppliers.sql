-- +goose Up

CREATE TABLE suppliers (
  id                  BIGSERIAL     PRIMARY KEY,
  name                VARCHAR(150)  NOT NULL,
  contact_person      VARCHAR(100),
  phone               VARCHAR(15),
  address             TEXT,
  credit_days         INT           NOT NULL DEFAULT 30,
  outstanding_balance DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  is_active           BOOLEAN       NOT NULL DEFAULT true,
  created_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_suppliers_is_active ON suppliers (is_active);

-- +goose StatementBegin
CREATE TYPE purchase_status AS ENUM ('draft', 'received', 'paid', 'partial');
-- +goose StatementEnd

CREATE TABLE purchases (
  id          BIGSERIAL       PRIMARY KEY,
  supplier_id BIGINT          NOT NULL REFERENCES suppliers (id) ON DELETE RESTRICT,
  invoice_no  VARCHAR(50),
  status      purchase_status NOT NULL DEFAULT 'draft',
  subtotal    DECIMAL(14,2)   NOT NULL DEFAULT 0.00,
  discount    DECIMAL(14,2)   NOT NULL DEFAULT 0.00,
  total       DECIMAL(14,2)   NOT NULL DEFAULT 0.00,
  paid_amount DECIMAL(14,2)   NOT NULL DEFAULT 0.00,
  due_date    DATE,
  received_by BIGINT          REFERENCES users (id) ON DELETE SET NULL,
  notes       TEXT,
  created_at  TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_purchases_supplier_id ON purchases (supplier_id);
CREATE INDEX idx_purchases_status      ON purchases (status);
CREATE INDEX idx_purchases_due_date    ON purchases (due_date);

CREATE TABLE purchase_items (
  id            BIGSERIAL     PRIMARY KEY,
  purchase_id   BIGINT        NOT NULL REFERENCES purchases (id) ON DELETE CASCADE,
  product_id    BIGINT        NOT NULL REFERENCES products  (id) ON DELETE RESTRICT,
  quantity      DECIMAL(12,3) NOT NULL,
  cost_price    DECIMAL(12,2) NOT NULL,
  selling_price DECIMAL(12,2) NOT NULL,
  expiry_date   DATE,
  subtotal      DECIMAL(14,2) NOT NULL
);
CREATE INDEX idx_purchase_items_purchase_id ON purchase_items (purchase_id);
CREATE INDEX idx_purchase_items_product_id  ON purchase_items (product_id);
CREATE INDEX idx_purchase_items_expiry_date ON purchase_items (expiry_date);

-- +goose Down
DROP TABLE purchase_items;
DROP TABLE purchases;
DROP TABLE suppliers;
DROP TYPE purchase_status;
