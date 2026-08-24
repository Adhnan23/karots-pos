-- +goose Up
-- Admin-defined custom product fields (global to all products) and their values.
-- Absence of a pp_values row means "use the field's default", so enabling this on
-- a live database never rewrites existing products.
CREATE TABLE pp_fields (
    id            BIGSERIAL PRIMARY KEY,
    key           TEXT NOT NULL UNIQUE,
    label         TEXT NOT NULL,
    type          TEXT NOT NULL CHECK (type IN ('text','number','bool','select')),
    default_value TEXT NOT NULL DEFAULT '',
    required      BOOLEAN NOT NULL DEFAULT false,
    searchable    BOOLEAN NOT NULL DEFAULT false,
    options       JSONB,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- product_id references core products(id) but carries NO foreign key: the plugin
-- schema versions independently and must not constrain core tables.
CREATE TABLE pp_values (
    field_id   BIGINT NOT NULL REFERENCES pp_fields(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (field_id, product_id)
);
CREATE INDEX idx_pp_values_product ON pp_values (product_id);
-- pg_trgm is already enabled by core; this keeps searchable substring matches fast.
CREATE INDEX idx_pp_values_value_trgm ON pp_values USING gin (value gin_trgm_ops);

-- +goose Down
DROP TABLE pp_values;
DROP TABLE pp_fields;
