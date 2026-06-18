-- +goose Up
-- Mobile-recharge plugin schema. All cross-references to core tables (products,
-- cash-register sessions) are SOFT (plain BIGINT, no FK) so the plugin's
-- migrations stay independent of core's schema and can be applied to an existing
-- live database without ordering constraints. Additive only — never a wipe.

-- A carrier (Dialog, Mobitel, …). product_id points at the hidden is_service
-- core product that carries recharge sales for this carrier.
CREATE TABLE recharge_carriers (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    product_id BIGINT NOT NULL,
    is_active  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Optional reload devices/SIMs under a carrier (labels only).
CREATE TABLE recharge_devices (
    id         BIGSERIAL PRIMARY KEY,
    carrier_id BIGINT NOT NULL REFERENCES recharge_carriers(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    number     TEXT
);

-- Mid-shift airtime top-ups bought from the carrier (an outgoing float cost).
-- expense_id points at the core expense row created for the top-up.
CREATE TABLE recharge_topups (
    id         BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL,
    carrier_id BIGINT NOT NULL REFERENCES recharge_carriers(id),
    amount     NUMERIC(12,2) NOT NULL,
    expense_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-carrier reconciliation for one cash-drawer session. bonus_loss is the
-- carrier's promo bonus (or loss): closing - opening + sold - topups.
CREATE TABLE recharge_carrier_sessions (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT NOT NULL,
    carrier_id   BIGINT NOT NULL REFERENCES recharge_carriers(id),
    opening      NUMERIC(12,2) NOT NULL DEFAULT 0,
    topups_total NUMERIC(12,2) NOT NULL DEFAULT 0,
    sold_total   NUMERIC(12,2) NOT NULL DEFAULT 0,
    closing      NUMERIC(12,2) NOT NULL DEFAULT 0,
    bonus_loss   NUMERIC(12,2) NOT NULL DEFAULT 0,
    opened_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at    TIMESTAMPTZ,
    UNIQUE (session_id, carrier_id)
);

-- +goose Down
DROP TABLE recharge_carrier_sessions;
DROP TABLE recharge_topups;
DROP TABLE recharge_devices;
DROP TABLE recharge_carriers;
