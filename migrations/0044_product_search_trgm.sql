-- Forgiving product search.
--
-- The old search was a single substring match on the whole query, so a cashier
-- had to type a contiguous slice of the name in the right order: "yellow" found
-- "Bigo Trivago Yellow Flip Flop Size 4", and so did "size 4", but
-- "yellow flip flop size 4" found nothing. Searching is now token-based (every
-- word must appear, in any order) with a trigram fallback for typos.
--
-- pg_trgm also backs the GIN indexes below, which make the '%tok%' LIKE
-- patterns indexable — a leading wildcard cannot use a normal B-tree.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Matches ILIKE '%tok%' and word_similarity() against the product name.
CREATE INDEX IF NOT EXISTS idx_products_name_trgm
	ON products USING gin (lower(name) gin_trgm_ops);

-- Matches the "squashed" form, so the token "flipflop" still finds "Flip Flop".
-- Keep this expression identical to squashedName in internal/features/products.
CREATE INDEX IF NOT EXISTS idx_products_name_squashed_trgm
	ON products USING gin (translate(lower(name), ' -_/.', '') gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS idx_products_name_squashed_trgm;
DROP INDEX IF EXISTS idx_products_name_trgm;
-- pg_trgm is left installed: other objects may come to depend on it, and
-- dropping an extension is not a safe automatic rollback.
