-- +goose Up
-- A thin, balance-free log of bill payments / get-money done at the till against a
-- core kind="bank" locker. The MONEY itself lives entirely in core (cashflow.Move
-- writes the CR- receipts + locker ledger); this table only records the
-- customer-facing detail so the slip can be reprinted and the movement can be
-- listed in the recharge "Bill" receipts tab. No balance is tracked here — the
-- bank balance is the core locker's.
CREATE TABLE recharge_bill_tx (
    id             BIGSERIAL PRIMARY KEY,
    session_id     BIGINT,
    bank_locker_id BIGINT NOT NULL,
    bank_name      TEXT NOT NULL,
    type           TEXT NOT NULL CHECK (type IN ('billpay','getmoney')),
    amount         NUMERIC(12,2) NOT NULL,
    service_charge NUMERIC(12,2) NOT NULL DEFAULT 0,
    reference      TEXT,
    note           TEXT,
    created_by     BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_recharge_bill_tx_session ON recharge_bill_tx (session_id);

-- +goose Down
DROP TABLE recharge_bill_tx;
