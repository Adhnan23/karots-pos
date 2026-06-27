-- +goose Up
-- The post-sale print-prompt flag now governs payment/transfer receipts too, not
-- just sales, so rename it to a name that reflects its broader meaning.
ALTER TABLE settings RENAME COLUMN prompt_after_sale TO ask_to_print;

-- +goose Down
ALTER TABLE settings RENAME COLUMN ask_to_print TO prompt_after_sale;
