-- +goose Up
-- Money receipts: one tracked, printable, searchable receipt per money movement
-- (a locker transfer, supplier/customer payment, expense, refund, ...). Every
-- cashflow.Move writes exactly one row here in the same transaction as the ledger
-- legs, so a receipt can never exist without its money move or vice-versa. The
-- receipt_no is a human-friendly unique number (CR-000042) mirroring sale S- numbers.
CREATE SEQUENCE money_receipt_seq;

CREATE TABLE money_receipts (
	id          BIGSERIAL PRIMARY KEY,
	receipt_no  TEXT          NOT NULL UNIQUE,           -- 'CR-000042'
	kind        VARCHAR(24)   NOT NULL,                  -- transfer | supplier_payment | customer_payment | expense | refund | capital | bank_charge | interest | adjust | intake | payment
	from_label  TEXT          NOT NULL DEFAULT '',       -- 'Shop safe' | 'Till — Kasun' | 'External'
	to_label    TEXT          NOT NULL DEFAULT '',
	party       TEXT          NOT NULL DEFAULT '',        -- outside party (customer/supplier) name, for search
	amount      NUMERIC(14,2) NOT NULL,
	note        TEXT          NOT NULL DEFAULT '',
	ref_kind    VARCHAR(24),                              -- soft link to the domain row (expense, supplier_payment, ...)
	ref_id      BIGINT,
	created_by  BIGINT,
	created_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX idx_money_receipts_created ON money_receipts (created_at DESC);
CREATE INDEX idx_money_receipts_kind    ON money_receipts (kind);

-- +goose Down
DROP TABLE money_receipts;
DROP SEQUENCE money_receipt_seq;
