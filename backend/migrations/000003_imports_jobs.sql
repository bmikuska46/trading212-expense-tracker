CREATE TABLE csv_mappings (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL,
    format_fingerprint TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    mapping_json TEXT NOT NULL CHECK (json_valid(mapping_json)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (source, format_fingerprint, version)
) STRICT;

CREATE TABLE import_batches (
    id INTEGER PRIMARY KEY,
    source_account_id INTEGER NOT NULL REFERENCES accounts(id),
    mapping_id INTEGER REFERENCES csv_mappings(id),
    mapping_snapshot_json TEXT CHECK (json_valid(mapping_snapshot_json)),
    filename TEXT NOT NULL,
    period_start TEXT,
    period_end TEXT,
    status TEXT NOT NULL DEFAULT 'staging' CHECK (status IN ('staging', 'validating', 'failed', 'committed')),
    review_status TEXT NOT NULL DEFAULT 'not_required' CHECK (review_status IN ('not_required', 'needs_review', 'confirmed')),
    row_count INTEGER NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    inserted_count INTEGER NOT NULL DEFAULT 0 CHECK (inserted_count >= 0),
    excluded_count INTEGER NOT NULL DEFAULT 0 CHECK (excluded_count >= 0),
    duplicate_count INTEGER NOT NULL DEFAULT 0 CHECK (duplicate_count >= 0),
    error_count INTEGER NOT NULL DEFAULT 0 CHECK (error_count >= 0),
    validation_errors_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(validation_errors_json)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    committed_at TEXT,
    confirmed_at TEXT,
    CHECK ((period_start IS NULL AND period_end IS NULL) OR (period_start IS NOT NULL AND period_end IS NOT NULL AND period_end > period_start))
) STRICT;
CREATE INDEX import_batches_account ON import_batches(source_account_id, id);
CREATE INDEX import_batches_review_period ON import_batches(review_status, period_start, period_end);
CREATE INDEX import_batches_status ON import_batches(status, id);

CREATE TABLE staging_rows (
    batch_id INTEGER NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
    row_number INTEGER NOT NULL CHECK (row_number > 0),
    raw_record_json TEXT NOT NULL CHECK (json_valid(raw_record_json)),
    parsed_record_json TEXT CHECK (json_valid(parsed_record_json)),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'valid', 'invalid', 'excluded', 'duplicate', 'conflict')),
    validation_errors_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(validation_errors_json)),
    PRIMARY KEY (batch_id, row_number)
) STRICT;
CREATE INDEX staging_rows_status ON staging_rows(batch_id, status, row_number);

CREATE TABLE source_conflicts (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES transactions(id),
    batch_id INTEGER NOT NULL REFERENCES import_batches(id),
    row_number INTEGER NOT NULL,
    incoming_record_json TEXT NOT NULL CHECK (json_valid(incoming_record_json)),
    resolved_at TEXT,
    UNIQUE (batch_id, row_number),
    FOREIGN KEY (batch_id, row_number) REFERENCES staging_rows(batch_id, row_number)
) STRICT;
CREATE INDEX source_conflicts_transaction ON source_conflicts(transaction_id, resolved_at);

CREATE TABLE exchange_rates (
    base_currency TEXT NOT NULL DEFAULT 'EUR' CHECK (base_currency = 'EUR'),
    quote_currency TEXT NOT NULL CHECK (quote_currency GLOB '[A-Z][A-Z][A-Z]'),
    publication_date TEXT NOT NULL CHECK (length(publication_date) = 10),
    rate TEXT NOT NULL CHECK (
        length(rate) BETWEEN 1 AND 128 AND rate NOT GLOB '*[^0-9.]*' AND
        substr(rate, 1, 1) GLOB '[0-9]' AND substr(rate, -1, 1) GLOB '[0-9]' AND
        length(rate) - length(replace(rate, '.', '')) <= 1 AND
        replace(replace(rate, '0', ''), '.', '') != ''
    ),
    provider_policy TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    PRIMARY KEY (quote_currency, publication_date)
) STRICT;

CREATE TABLE scheduled_jobs (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('monthly_import', 'daily_rates')),
    source_account_id INTEGER REFERENCES accounts(id),
    period_key TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'Europe/Bratislava',
    period_start_utc TEXT,
    period_end_utc TEXT,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'requesting', 'polling', 'downloading', 'validating', 'committing', 'completed', 'failed', 'canceled')),
    report_id TEXT,
    report_status TEXT,
    batch_id INTEGER REFERENCES import_batches(id),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TEXT NOT NULL,
    last_error_code TEXT,
    last_error_detail TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    CHECK ((kind = 'monthly_import' AND source_account_id IS NOT NULL AND period_start_utc IS NOT NULL AND period_end_utc IS NOT NULL AND period_end_utc > period_start_utc) OR
           (kind = 'daily_rates' AND source_account_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX scheduled_jobs_monthly_period ON scheduled_jobs(kind, source_account_id, period_key) WHERE kind = 'monthly_import';
CREATE UNIQUE INDEX scheduled_jobs_daily_period ON scheduled_jobs(kind, period_key) WHERE kind = 'daily_rates';
CREATE INDEX scheduled_jobs_due ON scheduled_jobs(state, next_attempt_at);

CREATE TABLE job_attempts (
    id INTEGER PRIMARY KEY,
    job_id INTEGER NOT NULL REFERENCES scheduled_jobs(id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    stage TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    outcome TEXT CHECK (outcome IN ('succeeded', 'transient_failure', 'permanent_failure', 'uncertain')),
    error_code TEXT,
    error_detail TEXT,
    UNIQUE (job_id, attempt_number)
) STRICT;

CREATE TABLE notification_events (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES import_batches(id),
    kind TEXT NOT NULL CHECK (kind IN ('import_review', 'import_failure')),
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    idempotency_key TEXT NOT NULL UNIQUE,
    delivery_state TEXT NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending', 'sending', 'delivered', 'failed', 'uncertain')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TEXT NOT NULL,
    first_attempt_at TEXT,
    email_id TEXT,
    last_error_code TEXT,
    last_error_detail TEXT,
    dismissed_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (batch_id, kind)
) STRICT;
CREATE INDEX notification_events_due ON notification_events(delivery_state, next_attempt_at);
CREATE INDEX notification_events_active ON notification_events(dismissed_at, batch_id);

CREATE TABLE notification_attempts (
    id INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES notification_events(id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    outcome TEXT CHECK (outcome IN ('delivered', 'transient_failure', 'permanent_failure', 'uncertain')),
    email_id TEXT,
    error_code TEXT,
    error_detail TEXT,
    UNIQUE (event_id, attempt_number)
) STRICT;
