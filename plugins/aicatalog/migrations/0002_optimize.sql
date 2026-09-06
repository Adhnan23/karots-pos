-- +goose Up
-- One row per applied Optimize run, holding the list of undo operations that
-- exactly reverse it (so "Revert last optimize" restores the prior state).
CREATE TABLE aicatalog_optimize_runs (
  id          BIGSERIAL PRIMARY KEY,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  applied_by  BIGINT,
  summary     TEXT NOT NULL DEFAULT '',
  undo        JSONB NOT NULL,
  reverted_at TIMESTAMPTZ
);

-- +goose Down
DROP TABLE aicatalog_optimize_runs;
