-- +goose Up
-- Retire the plugin-owned bank-card subsystem. A "bank" is now a core kind="bank"
-- locker (managed under Money → Cash Lockers); bill-pay / get-money move money
-- through cashflow.Move so every leg gets a CR- receipt and shows in core Cash
-- Flow. The plugin no longer tracks a bank balance of its own. No data migration —
-- demo data starts fresh on core lockers.
DROP TABLE IF EXISTS recharge_card_tx;
DROP TABLE IF EXISTS recharge_bank_cards;

-- +goose Down
-- Recreate empty shells so a rollback leaves a consistent schema (history is not
-- restored — the tables were retired, not migrated).
CREATE TABLE recharge_bank_cards (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    is_active  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE recharge_card_tx (
    id             BIGSERIAL PRIMARY KEY,
    card_id        BIGINT NOT NULL REFERENCES recharge_bank_cards(id) ON DELETE CASCADE,
    session_id     BIGINT,
    type           TEXT NOT NULL CHECK (type IN ('billpay','getmoney','deposit','withdrawal')),
    amount         NUMERIC(12,2) NOT NULL,
    balance_delta  NUMERIC(12,2) NOT NULL DEFAULT 0,
    cash_delta     NUMERIC(12,2) NOT NULL DEFAULT 0,
    service_charge NUMERIC(12,2) NOT NULL DEFAULT 0,
    reference      TEXT,
    note           TEXT,
    created_by     BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_recharge_card_tx_card ON recharge_card_tx (card_id);
