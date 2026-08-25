-- +goose Up
CREATE TABLE alt_groups (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    sort_order INT  NOT NULL DEFAULT 0,
    is_active  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alt_tiers (
    id            BIGSERIAL PRIMARY KEY,
    group_id      BIGINT NOT NULL REFERENCES alt_groups(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    reorder_level INT  NOT NULL DEFAULT 0,
    sort_order    INT  NOT NULL DEFAULT 0,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE INDEX idx_alt_tiers_group ON alt_tiers(group_id);

CREATE TABLE alt_members (
    product_id BIGINT PRIMARY KEY,                 -- exactly-one membership
    tier_id    BIGINT NOT NULL REFERENCES alt_tiers(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_alt_members_tier ON alt_members(tier_id);

-- +goose Down
DROP TABLE alt_members;
DROP TABLE alt_tiers;
DROP TABLE alt_groups;
