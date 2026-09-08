-- +goose Up
CREATE TABLE repair_jobs (
    id             BIGSERIAL PRIMARY KEY,
    ticket_no      TEXT NOT NULL,
    ticket_code    TEXT NOT NULL UNIQUE,
    customer_id    BIGINT,
    customer_name  TEXT NOT NULL DEFAULT '',
    customer_phone TEXT NOT NULL DEFAULT '',
    repair_type    TEXT NOT NULL DEFAULT '',
    device_model   TEXT NOT NULL DEFAULT '',
    fault          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'received',
    promised_date  DATE,
    urgent         BOOLEAN NOT NULL DEFAULT false,
    warranty_days  INT NOT NULL DEFAULT 0,
    warranty_until DATE,
    sale_id        BIGINT,
    rework_of      BIGINT,
    notes          TEXT NOT NULL DEFAULT '',
    created_by     BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at       TIMESTAMPTZ,
    collected_at   TIMESTAMPTZ,
    cancelled_at   TIMESTAMPTZ
);
CREATE INDEX repair_jobs_status_idx ON repair_jobs (status);

CREATE TABLE repair_parts (
    id             BIGSERIAL PRIMARY KEY,
    job_id         BIGINT NOT NULL REFERENCES repair_jobs(id) ON DELETE CASCADE,
    product_id     BIGINT NOT NULL,
    qty            NUMERIC NOT NULL DEFAULT 1,
    unit_charge    NUMERIC NOT NULL DEFAULT 0,
    discount       NUMERIC NOT NULL DEFAULT 0,
    discount_type  TEXT NOT NULL DEFAULT 'fixed',
    discount_value NUMERIC NOT NULL DEFAULT 0
);

CREATE TABLE repair_charges (
    id     BIGSERIAL PRIMARY KEY,
    job_id BIGINT NOT NULL REFERENCES repair_jobs(id) ON DELETE CASCADE,
    label  TEXT NOT NULL,
    amount NUMERIC NOT NULL DEFAULT 0
);

CREATE TABLE repair_payments (
    id         BIGSERIAL PRIMARY KEY,
    job_id     BIGINT NOT NULL REFERENCES repair_jobs(id) ON DELETE CASCADE,
    amount     NUMERIC NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'deposit',
    user_id    BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Single-row config: the hidden labour/service product + the default warranty.
CREATE TABLE repair_config (
    id                    INT PRIMARY KEY DEFAULT 1,
    labour_product_id     BIGINT NOT NULL DEFAULT 0,
    default_warranty_days INT NOT NULL DEFAULT 0,
    ticket_seq            BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT repair_config_singleton CHECK (id = 1)
);
INSERT INTO repair_config (id) VALUES (1);

-- +goose Down
DROP TABLE repair_payments;
DROP TABLE repair_charges;
DROP TABLE repair_parts;
DROP TABLE repair_config;
DROP TABLE repair_jobs;
