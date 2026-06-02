-- +goose Up

-- settings: single-row shop configuration (id is always 1, enforced by CHECK).
CREATE TABLE settings (
  id                INT           PRIMARY KEY DEFAULT 1,
  shop_name         VARCHAR(150)  NOT NULL DEFAULT 'My Shop',
  shop_name_si      VARCHAR(150),
  address           TEXT,
  phone             VARCHAR(15),
  currency_code     VARCHAR(10)   NOT NULL DEFAULT 'LKR',
  currency_symbol   VARCHAR(5)    NOT NULL DEFAULT 'Rs.',
  receipt_footer    TEXT          DEFAULT 'Thank you for your purchase!',
  logo_url          TEXT,
  tax_registered    BOOLEAN       NOT NULL DEFAULT false,
  tax_reg_no        VARCHAR(30),
  low_stock_alerts  BOOLEAN       NOT NULL DEFAULT true,
  default_sale_type sale_type     NOT NULL DEFAULT 'retail',
  updated_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  CONSTRAINT settings_single_row CHECK (id = 1)
);

INSERT INTO settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION touch_settings_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_settings_updated_at
  BEFORE UPDATE ON settings
  FOR EACH ROW EXECUTE FUNCTION touch_settings_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS trg_settings_updated_at ON settings;
DROP FUNCTION IF EXISTS touch_settings_updated_at();
DROP TABLE settings;
