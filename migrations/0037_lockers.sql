-- +goose Up
-- Core cash lockers: named money-holding locations outside the cashier drawers
-- (a safe, a bank account, the owner's pocket, ...). Each locker's balance is the
-- running SUM of its ledger deltas — never a stored column — so it can never drift
-- from its history. allow_negative lets owner-pocket / "owner's brother" style
-- lockers go below zero (he may put in more than he takes); safe/bank are guarded
-- against overdraw once cash moves are wired (slice 2+).
CREATE TABLE lockers (
	id             BIGSERIAL PRIMARY KEY,
	name           TEXT        NOT NULL UNIQUE,
	kind           VARCHAR(10) NOT NULL DEFAULT 'other' CHECK (kind IN ('safe','bank','pocket','other')),
	allow_negative BOOLEAN     NOT NULL DEFAULT false,
	is_active      BOOLEAN     NOT NULL DEFAULT true,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only money ledger for lockers. balance = SUM(balance_delta) per locker.
-- counterparty + counter_* describe the far side of a move (a till session or
-- another locker, or an external trading party); ref_kind/ref_id softly link the
-- row back to the domain record that caused it (expense, supplier_payment, ...).
CREATE TABLE locker_ledger (
	id                   BIGSERIAL PRIMARY KEY,
	locker_id            BIGINT        NOT NULL REFERENCES lockers (id),
	balance_delta        NUMERIC(14,2) NOT NULL,
	kind                 VARCHAR(16)   NOT NULL CHECK (kind IN ('open_balance','transfer','payment','intake','bank_charge','interest','adjust')),
	counterparty         VARCHAR(10)            CHECK (counterparty IN ('till','locker','external')),
	counter_till_session BIGINT,
	counter_locker_id    BIGINT                 REFERENCES lockers (id),
	ref_kind             VARCHAR(20),
	ref_id               BIGINT,
	note                 TEXT          NOT NULL DEFAULT '',
	created_by           BIGINT,
	created_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX idx_locker_ledger_locker ON locker_ledger (locker_id);

-- +goose Down
DROP TABLE locker_ledger;
DROP TABLE lockers;
