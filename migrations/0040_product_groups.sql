-- +goose Up
CREATE TABLE product_groups (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(80) NOT NULL,
    emoji      VARCHAR(16),
    parent_id  BIGINT REFERENCES product_groups(id) ON DELETE CASCADE,
    sort_order INT NOT NULL DEFAULT 0,
    is_active  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_product_groups_parent ON product_groups(parent_id);

CREATE TABLE product_group_items (
    group_id   BIGINT NOT NULL REFERENCES product_groups(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id)       ON DELETE CASCADE,
    emoji      VARCHAR(16),
    sort_order INT NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, product_id)
);
CREATE INDEX idx_pgi_product ON product_group_items(product_id);

-- +goose Down
DROP TABLE product_group_items;
DROP TABLE product_groups;
