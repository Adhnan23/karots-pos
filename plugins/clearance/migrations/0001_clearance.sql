-- +goose Up
CREATE TABLE clearance_markdowns (
    product_id     BIGINT PRIMARY KEY,
    discount_type  TEXT NOT NULL DEFAULT 'percent',  -- percent | fixed
    discount_value NUMERIC NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'approved',  -- approved | dismissed
    approved_by    BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE clearance_settings (
    id                 INTEGER PRIMARY KEY DEFAULT 1,
    stale_days         INTEGER NOT NULL DEFAULT 60,
    default_percent    NUMERIC NOT NULL DEFAULT 20,
    min_margin_percent NUMERIC NOT NULL DEFAULT 5,
    CONSTRAINT clearance_settings_singleton CHECK (id = 1)
);
INSERT INTO clearance_settings (id) VALUES (1);

-- +goose Down
DROP TABLE clearance_settings;
DROP TABLE clearance_markdowns;
