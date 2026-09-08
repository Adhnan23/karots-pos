-- +goose Up
-- Who did the repair (free text w/ datalist; blank / "us" means in-house). When
-- an outside person does it, the admin pays them a cost booked through core
-- Expenses; repairer_paid tracks how much has been paid out, for display.
ALTER TABLE repair_jobs ADD COLUMN repaired_by   TEXT    NOT NULL DEFAULT '';
ALTER TABLE repair_jobs ADD COLUMN repairer_paid NUMERIC NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE repair_jobs DROP COLUMN repairer_paid;
ALTER TABLE repair_jobs DROP COLUMN repaired_by;
