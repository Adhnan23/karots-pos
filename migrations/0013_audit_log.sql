-- +goose Up

-- Audit trail: who did what. Written best-effort by the web layer (and the cash
-- API) after a mutation succeeds. user_name is snapshotted so the record stays
-- meaningful even if the user is later renamed or removed.
CREATE TABLE audit_log (
  id         BIGSERIAL    PRIMARY KEY,
  user_id    BIGINT       REFERENCES users (id) ON DELETE SET NULL,
  user_name  VARCHAR(120) NOT NULL DEFAULT '',
  action     VARCHAR(40)  NOT NULL,   -- create/update/delete/void/return/payment/withdraw/close/settings/backup/restore
  entity     VARCHAR(40)  NOT NULL,   -- product/customer/supplier/sale/denomination/user/settings/cash/system
  entity_id  VARCHAR(40),
  detail     TEXT,
  created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_created ON audit_log (created_at DESC);
CREATE INDEX idx_audit_entity  ON audit_log (entity);
CREATE INDEX idx_audit_user    ON audit_log (user_id);

-- +goose Down
DROP TABLE audit_log;
