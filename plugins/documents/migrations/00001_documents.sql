-- +goose Up

-- A sellable communication-store service. `metered` services are priced from the
-- doc_price matrix (by size/colour/side) with quantity tiers and consume paper/
-- film; `custom` services (photo editing, CV, posters) are typed-price labour
-- jobs. Each service owns a hidden is_service core product so its sales flow
-- through the normal till/receipt/drawer.
CREATE TABLE doc_service (
  id         BIGSERIAL    PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  kind       VARCHAR(10)  NOT NULL DEFAULT 'metered',  -- metered | custom
  category   VARCHAR(20)  NOT NULL DEFAULT 'other',    -- copy|print|laminate|bind|other
  product_id BIGINT       NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
  is_active  BOOLEAN      NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_doc_service_active ON doc_service (is_active);

-- Price matrix + quantity tiers for metered services. A job matches the row for
-- (service, size, colour, double_side) with the greatest min_qty <= ordered qty.
-- Flat pricing = a single row with size NULL.
CREATE TABLE doc_price (
  id          BIGSERIAL     PRIMARY KEY,
  service_id  BIGINT        NOT NULL REFERENCES doc_service (id) ON DELETE CASCADE,
  size        VARCHAR(20),                              -- A4/A3/Legal; NULL = any
  color       BOOLEAN       NOT NULL DEFAULT false,
  double_side BOOLEAN       NOT NULL DEFAULT false,
  min_qty     INT           NOT NULL DEFAULT 1,
  unit_price  DECIMAL(12,2) NOT NULL
);
CREATE INDEX idx_doc_price_service ON doc_price (service_id);

-- Consumable mapping: stock (a core paper/film product) drawn down per impression
-- for a service+size. Double-sided halves the sheets via the ceil rule at runtime.
CREATE TABLE doc_consumable (
  id           BIGSERIAL     PRIMARY KEY,
  service_id   BIGINT        NOT NULL REFERENCES doc_service (id) ON DELETE CASCADE,
  size         VARCHAR(20),                             -- NULL = any
  product_id   BIGINT        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
  qty_per_unit DECIMAL(12,4) NOT NULL DEFAULT 1
);
CREATE INDEX idx_doc_consumable_service ON doc_consumable (service_id);

-- Per-job ledger linked to its sale: analytics (revenue/consumable cost) + labour.
CREATE TABLE doc_job (
  id               BIGSERIAL     PRIMARY KEY,
  sale_id          BIGINT        REFERENCES sales (id) ON DELETE SET NULL,
  service_id       BIGINT        REFERENCES doc_service (id) ON DELETE SET NULL,
  description      TEXT          NOT NULL DEFAULT '',
  qty              DECIMAL(12,3) NOT NULL DEFAULT 1,
  unit_price       DECIMAL(12,2) NOT NULL DEFAULT 0,
  line_total       DECIMAL(14,2) NOT NULL DEFAULT 0,
  consumable_cost  DECIMAL(14,2) NOT NULL DEFAULT 0,
  labour_worker_id BIGINT        REFERENCES users (id) ON DELETE SET NULL,
  labour_amount    DECIMAL(12,2) NOT NULL DEFAULT 0,
  labour_payout_id BIGINT,                              -- set once settled
  created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_doc_job_sale   ON doc_job (sale_id);
CREATE INDEX idx_doc_job_worker ON doc_job (labour_worker_id);
CREATE INDEX idx_doc_job_created ON doc_job (created_at);

-- Worker payout settlements. amount = sum of the unpaid labour it covers; the
-- core expense it booked is referenced by expense_id.
CREATE TABLE doc_payout (
  id         BIGSERIAL     PRIMARY KEY,
  worker_id  BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  amount     DECIMAL(14,2) NOT NULL,
  expense_id BIGINT,
  paid_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_doc_payout_worker ON doc_payout (worker_id);

-- +goose Down
DROP TABLE doc_payout;
DROP TABLE doc_job;
DROP TABLE doc_consumable;
DROP TABLE doc_price;
DROP TABLE doc_service;
