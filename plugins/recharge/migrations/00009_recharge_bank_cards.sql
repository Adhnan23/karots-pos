-- +goose Up
-- Bank cards are a FIRST-CLASS, carrier-independent money source: the shop's own
-- debit/credit card or bank account used to pay bills and hand out cash, with a
-- real running balance the shop tracks. They are no longer modelled as a
-- tracks_float=false device under a carrier (which wrongly coupled them to airtime
-- reloads). All cross-references to core (cash sessions, users) are SOFT BIGINTs
-- with no FK so the plugin stays schema-independent. Additive / late-enable safe.
CREATE TABLE recharge_bank_cards (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    is_active  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Running-balance ledger for bank cards. balance_delta is the signed effect on the
-- card's tracked balance; cash_delta the signed effect on the cash drawer.
--   billpay    (cashier): balance −, cash +   (customer pays cash, bill paid by card)
--   getmoney   (cashier): balance +, cash −   (customer funds the account, shop pays cash)
--   deposit    (admin):   balance +, cash 0   (balance-only adjustment, no drawer)
--   withdrawal (admin):   balance −, cash 0   (balance-only adjustment, no drawer)
-- service_charge is always extra cash into the drawer (shop earnings) on cashier ops.
-- session_id is the core cash session (NULL for admin balance adjustments).
-- Card balance = SUM(balance_delta) all-time — a persistent bank balance, so there
-- is NO per-session opening/closing like the device floats.
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

-- Migrate any existing bank-card devices (tracks_float=false) into the new table
-- and retire them so they vanish from the device/money pickers. Their historical
-- recharge_transactions rows stay in place as history (they were balance-untracked).
INSERT INTO recharge_bank_cards (name, is_active)
    SELECT label, is_active FROM recharge_devices WHERE tracks_float = false
    ON CONFLICT (name) DO NOTHING;
UPDATE recharge_devices SET is_active = false WHERE tracks_float = false;

-- +goose Down
DROP TABLE recharge_card_tx;
DROP TABLE recharge_bank_cards;
