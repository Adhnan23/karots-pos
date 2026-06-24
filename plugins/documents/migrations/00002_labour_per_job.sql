-- +goose Up

-- Labour redesign: cashiers set ONLY the price on custom jobs (no worker, no
-- wage at the till — that was open to misuse). The admin settles labour per
-- individual job in the admin panel: a typed amount + optional note, paid either
-- "from a till" (which also records that drawer's cash withdrawal) or "external".
-- Every payout books a core "Labour" expense. There is no worker attribution —
-- these services are rarely outsourced — so doc_payout decouples from users.
ALTER TABLE doc_payout ALTER COLUMN worker_id DROP NOT NULL;
ALTER TABLE doc_payout ADD COLUMN job_id       BIGINT REFERENCES doc_job (id) ON DELETE SET NULL;
ALTER TABLE doc_payout ADD COLUMN note         TEXT        NOT NULL DEFAULT '';
ALTER TABLE doc_payout ADD COLUMN source       VARCHAR(10) NOT NULL DEFAULT 'external'; -- external | till | none
ALTER TABLE doc_payout ADD COLUMN till_user_id BIGINT;
CREATE INDEX idx_doc_payout_job ON doc_payout (job_id);

-- +goose Down
DROP INDEX IF EXISTS idx_doc_payout_job;
ALTER TABLE doc_payout DROP COLUMN till_user_id;
ALTER TABLE doc_payout DROP COLUMN source;
ALTER TABLE doc_payout DROP COLUMN note;
ALTER TABLE doc_payout DROP COLUMN job_id;
ALTER TABLE doc_payout ALTER COLUMN worker_id SET NOT NULL;
