-- +goose Up

CREATE TABLE customers (
  id                  BIGSERIAL     PRIMARY KEY,
  name                VARCHAR(100)  NOT NULL,
  phone               VARCHAR(15),
  address             TEXT,
  credit_limit        DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  outstanding_balance DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  loyalty_points      INT           NOT NULL DEFAULT 0,
  is_active           BOOLEAN       NOT NULL DEFAULT true,
  created_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_customers_phone ON customers (phone);
CREATE INDEX idx_customers_name  ON customers (name);

-- +goose StatementBegin
CREATE TYPE sale_type   AS ENUM ('retail', 'wholesale', 'credit');
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TYPE sale_status AS ENUM ('completed', 'credit', 'returned', 'void');
-- +goose StatementEnd

-- Receipt numbers come from a dedicated sequence so concurrent sales can never
-- collide (the original plan had no atomic source for receipt_no).
CREATE SEQUENCE sales_receipt_seq START 1;

CREATE TABLE sales (
  id           BIGSERIAL     PRIMARY KEY,
  receipt_no   VARCHAR(20)   NOT NULL UNIQUE,
  customer_id  BIGINT        REFERENCES customers (id) ON DELETE SET NULL,
  sale_type    sale_type     NOT NULL DEFAULT 'retail',
  subtotal     DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  discount     DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  tax          DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  total        DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  paid_amount  DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  change_given DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  status       sale_status   NOT NULL DEFAULT 'completed',
  cashier_id   BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  notes        TEXT,
  created_at   TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_sales_customer_id ON sales (customer_id);
CREATE INDEX idx_sales_cashier_id  ON sales (cashier_id);
CREATE INDEX idx_sales_status      ON sales (status);
CREATE INDEX idx_sales_sale_type   ON sales (sale_type);
CREATE INDEX idx_sales_created_at  ON sales (created_at DESC);

CREATE TABLE sale_items (
  id         BIGSERIAL     PRIMARY KEY,
  sale_id    BIGINT        NOT NULL REFERENCES sales    (id) ON DELETE CASCADE,
  product_id BIGINT        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
  quantity   DECIMAL(12,3) NOT NULL,
  unit_price DECIMAL(12,2) NOT NULL,
  discount   DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  subtotal   DECIMAL(14,2) NOT NULL
);
CREATE INDEX idx_sale_items_sale_id    ON sale_items (sale_id);
CREATE INDEX idx_sale_items_product_id ON sale_items (product_id);

-- +goose StatementBegin
CREATE TYPE payment_method AS ENUM ('cash', 'card', 'online', 'credit');
-- +goose StatementEnd

CREATE TABLE payments (
  id         BIGSERIAL      PRIMARY KEY,
  sale_id    BIGINT         NOT NULL REFERENCES sales (id) ON DELETE CASCADE,
  method     payment_method NOT NULL,
  amount     DECIMAL(14,2)  NOT NULL,
  reference  VARCHAR(100),
  created_at TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_payments_sale_id ON payments (sale_id);
CREATE INDEX idx_payments_method  ON payments (method);

-- +goose Down
DROP TABLE payments;
DROP TABLE sale_items;
DROP TABLE sales;
DROP SEQUENCE sales_receipt_seq;
DROP TABLE customers;
DROP TYPE payment_method;
DROP TYPE sale_status;
DROP TYPE sale_type;
