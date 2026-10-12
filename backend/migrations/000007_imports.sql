-- The CSV pipeline records the parsed header and format of each upload.
-- staged_at is set once every record is in staging_rows, so a batch without
-- it was interrupted while streaming and is never validated or committed.
ALTER TABLE import_batches ADD COLUMN header_json TEXT CHECK (header_json IS NULL OR json_valid(header_json));
ALTER TABLE import_batches ADD COLUMN format_fingerprint TEXT;
ALTER TABLE import_batches ADD COLUMN staged_at TEXT;
ALTER TABLE import_batches ADD COLUMN conflict_count INTEGER NOT NULL DEFAULT 0 CHECK (conflict_count >= 0);
ALTER TABLE import_batches ADD COLUMN unknown_actions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(unknown_actions_json));

-- external_id is filled during validation to find repeated IDs in one file.
-- Owners can exclude a row or replace its values before the batch commits;
-- raw_record_json always keeps the record as it was uploaded.
ALTER TABLE staging_rows ADD COLUMN external_id TEXT;
ALTER TABLE staging_rows ADD COLUMN excluded_by_owner INTEGER NOT NULL DEFAULT 0 CHECK (excluded_by_owner IN (0, 1));
ALTER TABLE staging_rows ADD COLUMN corrected_record_json TEXT CHECK (corrected_record_json IS NULL OR json_valid(corrected_record_json));
CREATE INDEX staging_rows_external ON staging_rows(batch_id, external_id, row_number) WHERE external_id IS NOT NULL;

-- Canonical source values let a reimport compare amounts and instants
-- without depending on how the source formatted them.
ALTER TABLE imported_originals ADD COLUMN amount_minor INTEGER;
ALTER TABLE imported_originals ADD COLUMN occurred_at TEXT;
