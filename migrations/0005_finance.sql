-- +goose Up

CREATE TABLE expenses (
  id           BIGSERIAL     PRIMARY KEY,
  category     VARCHAR(80)   NOT NULL,
  amount       DECIMAL(12,2) NOT NULL,
  description  TEXT,
  paid_by      BIGINT        REFERENCES users (id) ON DELETE SET NULL,
  expense_date DATE          NOT NULL DEFAULT CURRENT_DATE,
  created_at   TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_expenses_category     ON expenses (category);
CREATE INDEX idx_expenses_expense_date ON expenses (expense_date DESC);
CREATE INDEX idx_expenses_paid_by      ON expenses (paid_by);

-- cash_register: daily drawer sessions. A partial unique index enforces at most
-- one open session per cashier.
CREATE TABLE cash_register (
  id            BIGSERIAL     PRIMARY KEY,
  user_id       BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  opening_cash  DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  closing_cash  DECIMAL(12,2),
  expected_cash DECIMAL(12,2),
  difference    DECIMAL(12,2),
  opened_at     TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  closed_at     TIMESTAMPTZ
);
CREATE INDEX idx_cash_register_user_id   ON cash_register (user_id);
CREATE INDEX idx_cash_register_opened_at ON cash_register (opened_at DESC);
CREATE UNIQUE INDEX idx_cash_register_open_session
  ON cash_register (user_id) WHERE closed_at IS NULL;

-- +goose Down
DROP TABLE cash_register;
DROP TABLE expenses;
