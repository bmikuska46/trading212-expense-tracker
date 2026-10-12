-- The linked source account for an integration (Trading 212) and the result of
-- the most recent connection check. Check results never contain secrets.
ALTER TABLE integrations ADD COLUMN source_account_id INTEGER REFERENCES accounts(id);
ALTER TABLE integrations ADD COLUMN check_json TEXT CHECK (check_json IS NULL OR json_valid(check_json));
ALTER TABLE integrations ADD COLUMN checked_at TEXT;
