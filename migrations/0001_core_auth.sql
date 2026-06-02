-- +goose Up
-- +goose StatementBegin
CREATE TYPE user_role AS ENUM ('admin', 'cashier', 'manager');
-- +goose StatementEnd

-- users: staff accounts. Login is via a bcrypt-hashed PIN.
CREATE TABLE users (
  id         BIGSERIAL    PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  phone      VARCHAR(15),
  role       user_role    NOT NULL DEFAULT 'cashier',
  pin_hash   VARCHAR(255) NOT NULL,
  is_active  BOOLEAN      NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_users_phone     ON users (phone);
CREATE INDEX idx_users_is_active ON users (is_active);

-- categories: self-referencing for sub-categories.
CREATE TABLE categories (
  id         BIGSERIAL   PRIMARY KEY,
  name       VARCHAR(80) NOT NULL,
  parent_id  BIGINT      REFERENCES categories (id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_categories_parent_id ON categories (parent_id);

-- units: units of measure.
CREATE TABLE units (
  id           BIGSERIAL   PRIMARY KEY,
  name         VARCHAR(30) NOT NULL UNIQUE,
  abbreviation VARCHAR(10) NOT NULL UNIQUE
);

INSERT INTO units (name, abbreviation) VALUES
  ('Piece',      'pcs'),
  ('Kilogram',   'kg'),
  ('Gram',       'g'),
  ('Litre',      'ltr'),
  ('Millilitre', 'ml'),
  ('Packet',     'pkt'),
  ('Bottle',     'btl'),
  ('Box',        'box'),
  ('Dozen',      'doz');

-- +goose Down
DROP TABLE categories;
DROP TABLE units;
DROP TABLE users;
DROP TYPE user_role;
