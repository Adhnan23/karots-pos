-- +goose Up
-- Mobile-money agent schema: per-device float reconciliation + a unified
-- transaction ledger for deposits, withdrawals, bill payments, supplier float
-- top-ups and wallet payments received for sales. Additive only (late-enable
-- safe); all cross-references to core (cash sessions, sales, expenses) are SOFT
-- BIGINTs with no FK so the plugin stays schema-independent of core. The
-- per-carrier tables from 00001 are left in place but no longer used.

-- Devices can be retired without losing history.
ALTER TABLE recharge_devices ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT TRUE;

-- Per-device opening/closing float counts for one cash-drawer session. The
-- cashier enters each physical device's counted balance directly (no mental
-- summing); the system aggregates per carrier. session_id is the core cash
-- session id (soft ref).
CREATE TABLE recharge_device_sessions (
    id         BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL,
    device_id  BIGINT NOT NULL REFERENCES recharge_devices(id) ON DELETE CASCADE,
    opening    NUMERIC(12,2) NOT NULL DEFAULT 0,
    closing    NUMERIC(12,2),
    opened_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at  TIMESTAMPTZ,
    UNIQUE (session_id, device_id)
);

-- Unified money-movement ledger. One row per agent transaction.
--   type        ∈ {deposit, withdrawal, billpay, topup, wallet_in}
--   float_delta  signed effect on the carrier float (+in / −out)
--   cash_delta   signed effect on the cash drawer (+to-drawer / −from-drawer)
-- Reloads are NOT recorded here — they flow through the core sale path as a
-- hidden is_service line and are summed from sale_items per carrier.
CREATE TABLE recharge_transactions (
    id          BIGSERIAL PRIMARY KEY,
    session_id  BIGINT NOT NULL,
    carrier_id  BIGINT NOT NULL REFERENCES recharge_carriers(id),
    device_id   BIGINT REFERENCES recharge_devices(id),
    type        TEXT NOT NULL CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in')),
    amount      NUMERIC(12,2) NOT NULL,
    cash_delta  NUMERIC(12,2) NOT NULL DEFAULT 0,
    float_delta NUMERIC(12,2) NOT NULL DEFAULT 0,
    sale_id     BIGINT,
    expense_id  BIGINT,
    reference   TEXT,
    note        TEXT,
    created_by  BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_recharge_tx_session ON recharge_transactions (session_id);
CREATE INDEX idx_recharge_tx_carrier ON recharge_transactions (carrier_id);

-- +goose Down
DROP TABLE recharge_transactions;
DROP TABLE recharge_device_sessions;
ALTER TABLE recharge_devices DROP COLUMN is_active;
