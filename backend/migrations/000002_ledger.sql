CREATE TABLE owner (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    email TEXT NOT NULL CHECK (length(trim(email)) > 0),
    password_hash TEXT NOT NULL CHECK (length(password_hash) > 0),
    timezone TEXT NOT NULL DEFAULT 'Europe/Bratislava',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE TABLE sessions (
    token_hash BLOB PRIMARY KEY CHECK (length(token_hash) = 32),
    owner_id INTEGER NOT NULL REFERENCES owner(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;
CREATE INDEX sessions_expiry ON sessions(expires_at);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL CHECK (json_valid(value_json)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE TABLE integrations (
    provider TEXT PRIMARY KEY CHECK (provider IN ('trading212', 'openai', 'resend')),
    credentials_json TEXT NOT NULL CHECK (json_valid(credentials_json)),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    earliest_automatic_period TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE TABLE accounts (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    currency TEXT NOT NULL CHECK (currency GLOB '[A-Z][A-Z][A-Z]'),
    integration_provider TEXT REFERENCES integrations(provider),
    external_account_id TEXT,
    opening_balance_minor INTEGER NOT NULL DEFAULT 0 CHECK (opening_balance_minor = 0),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (integration_provider, external_account_id),
    CHECK ((integration_provider IS NULL AND external_account_id IS NULL) OR
           (integration_provider IS NOT NULL AND external_account_id IS NOT NULL AND length(external_account_id) > 0))
) STRICT;

CREATE TABLE notification_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    provider TEXT NOT NULL DEFAULT 'resend' REFERENCES integrations(provider) CHECK (provider = 'resend'),
    sender TEXT NOT NULL,
    recipient TEXT NOT NULL,
    application_url TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1))
) STRICT;

CREATE TABLE categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    normalized_name TEXT NOT NULL UNIQUE CHECK (length(normalized_name) > 0)
) STRICT;
INSERT INTO categories (name, normalized_name) VALUES ('Uncategorized', 'uncategorized');

CREATE TABLE payees (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    normalized_name TEXT NOT NULL UNIQUE CHECK (length(normalized_name) > 0)
) STRICT;

CREATE TABLE category_aliases (
    normalized_name TEXT PRIMARY KEY CHECK (length(normalized_name) > 0),
    category_id INTEGER NOT NULL REFERENCES categories(id)
) STRICT;
CREATE INDEX category_aliases_target ON category_aliases(category_id);

CREATE TABLE payee_aliases (
    normalized_name TEXT PRIMARY KEY CHECK (length(normalized_name) > 0),
    payee_id INTEGER NOT NULL REFERENCES payees(id)
) STRICT;
CREATE INDEX payee_aliases_target ON payee_aliases(payee_id);

CREATE TABLE categorization_rules (
    id INTEGER PRIMARY KEY,
    payee_id INTEGER REFERENCES payees(id),
    merchant_pattern TEXT,
    category_id INTEGER NOT NULL REFERENCES categories(id),
    priority INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    CHECK (payee_id IS NOT NULL OR (merchant_pattern IS NOT NULL AND length(merchant_pattern) > 0))
) STRICT;
CREATE INDEX categorization_rules_payee ON categorization_rules(payee_id, enabled, priority);

CREATE TABLE transactions (
    id INTEGER PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    source_account_id INTEGER REFERENCES accounts(id),
    external_id TEXT,
    import_batch_id INTEGER REFERENCES import_batches(id),
    occurred_at TEXT NOT NULL,
    local_date TEXT NOT NULL CHECK (length(local_date) = 10),
    kind TEXT NOT NULL CHECK (kind IN ('expense', 'income', 'transfer', 'refund')),
    amount_minor INTEGER NOT NULL,
    currency TEXT NOT NULL CHECK (currency GLOB '[A-Z][A-Z][A-Z]'),
    minor_unit INTEGER NOT NULL CHECK (minor_unit BETWEEN 0 AND 4),
    eur_cents INTEGER NOT NULL,
    payee_id INTEGER REFERENCES payees(id),
    category_id INTEGER REFERENCES categories(id),
    refund_of_transaction_id INTEGER REFERENCES transactions(id),
    notes TEXT NOT NULL DEFAULT '',
    review_status TEXT NOT NULL DEFAULT 'confirmed' CHECK (review_status IN ('needs_review', 'confirmed', 'source_conflict')),
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (source_account_id, external_id),
    CHECK ((source_account_id IS NULL AND external_id IS NULL AND import_batch_id IS NULL) OR
           (source_account_id IS NOT NULL AND external_id IS NOT NULL AND length(trim(external_id)) > 0 AND import_batch_id IS NOT NULL)),
    CHECK (refund_of_transaction_id IS NULL OR (kind = 'refund' AND refund_of_transaction_id != id))
) STRICT;
CREATE INDEX transactions_date ON transactions(local_date, id);
CREATE INDEX transactions_account_date ON transactions(account_id, local_date, id);
CREATE INDEX transactions_payee_date ON transactions(payee_id, local_date, id);
CREATE INDEX transactions_category_date ON transactions(category_id, local_date, id);
CREATE INDEX transactions_batch ON transactions(import_batch_id, id);
CREATE INDEX transactions_review_date ON transactions(review_status, local_date, id);
CREATE INDEX transactions_refund ON transactions(refund_of_transaction_id);

CREATE TABLE transaction_splits (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES transactions(id),
    position INTEGER NOT NULL CHECK (position >= 0),
    category_id INTEGER NOT NULL REFERENCES categories(id),
    payee_id INTEGER REFERENCES payees(id),
    amount_minor INTEGER NOT NULL,
    eur_cents INTEGER NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    UNIQUE (transaction_id, position)
) STRICT;
CREATE INDEX transaction_splits_category ON transaction_splits(category_id, transaction_id);

CREATE TABLE linked_transfers (
    id INTEGER PRIMARY KEY
) STRICT;
CREATE TABLE transfer_legs (
    transfer_id INTEGER NOT NULL REFERENCES linked_transfers(id),
    transaction_id INTEGER NOT NULL UNIQUE REFERENCES transactions(id),
    direction TEXT NOT NULL CHECK (direction IN ('outgoing', 'incoming')),
    PRIMARY KEY (transfer_id, direction)
) STRICT;
CREATE TRIGGER transfer_legs_kind BEFORE INSERT ON transfer_legs
WHEN (SELECT kind FROM transactions WHERE id = NEW.transaction_id) != 'transfer'
BEGIN
    SELECT RAISE(ABORT, 'transfer leg requires a transfer transaction');
END;

CREATE TABLE imported_originals (
    transaction_id INTEGER PRIMARY KEY REFERENCES transactions(id),
    amount_text TEXT NOT NULL,
    currency TEXT NOT NULL CHECK (currency GLOB '[A-Z][A-Z][A-Z]'),
    timestamp_text TEXT NOT NULL,
    action TEXT NOT NULL,
    merchant_name TEXT NOT NULL DEFAULT '',
    merchant_category TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    fee_amount_text TEXT,
    fee_currency TEXT CHECK (fee_currency GLOB '[A-Z][A-Z][A-Z]'),
    raw_record_json TEXT NOT NULL CHECK (json_valid(raw_record_json)),
    CHECK ((fee_amount_text IS NULL) = (fee_currency IS NULL))
) STRICT;
CREATE TRIGGER imported_originals_source BEFORE INSERT ON imported_originals
WHEN (SELECT source_account_id FROM transactions WHERE id = NEW.transaction_id) IS NULL
BEGIN
    SELECT RAISE(ABORT, 'imported original requires a source transaction');
END;
CREATE TRIGGER imported_originals_no_update BEFORE UPDATE ON imported_originals
BEGIN
    SELECT RAISE(ABORT, 'imported originals are immutable');
END;
CREATE TRIGGER imported_originals_no_delete BEFORE DELETE ON imported_originals
BEGIN
    SELECT RAISE(ABORT, 'imported originals are immutable');
END;
CREATE TRIGGER transactions_source_immutable BEFORE UPDATE OF source_account_id, external_id, import_batch_id ON transactions
WHEN OLD.source_account_id IS NOT NEW.source_account_id OR OLD.external_id IS NOT NEW.external_id OR OLD.import_batch_id IS NOT NEW.import_batch_id
BEGIN
    SELECT RAISE(ABORT, 'transaction source identity is immutable');
END;

CREATE TABLE transaction_edit_history (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES transactions(id),
    revision INTEGER NOT NULL,
    snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json)),
    edited_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE INDEX transaction_edit_history_transaction ON transaction_edit_history(transaction_id, id);
CREATE TRIGGER transaction_edit_history_no_update BEFORE UPDATE ON transaction_edit_history
BEGIN
    SELECT RAISE(ABORT, 'transaction edit history is append-only');
END;
CREATE TRIGGER transaction_edit_history_no_delete BEFORE DELETE ON transaction_edit_history
BEGIN
    SELECT RAISE(ABORT, 'transaction edit history is append-only');
END;
CREATE TRIGGER transactions_capture_edit BEFORE UPDATE ON transactions
BEGIN
    INSERT INTO transaction_edit_history (transaction_id, revision, snapshot_json)
    VALUES (OLD.id, OLD.revision, json_object(
        'account_id', OLD.account_id, 'occurred_at', OLD.occurred_at, 'local_date', OLD.local_date,
        'kind', OLD.kind, 'amount_minor', OLD.amount_minor, 'currency', OLD.currency,
        'minor_unit', OLD.minor_unit, 'eur_cents', OLD.eur_cents, 'payee_id', OLD.payee_id,
        'category_id', OLD.category_id, 'refund_of_transaction_id', OLD.refund_of_transaction_id,
        'notes', OLD.notes, 'review_status', OLD.review_status,
        'splits', json((SELECT coalesce(json_group_array(json_object(
            'position', position, 'category_id', category_id, 'payee_id', payee_id,
            'amount_minor', amount_minor, 'eur_cents', eur_cents, 'notes', notes
        )), '[]') FROM transaction_splits WHERE transaction_id = OLD.id))
    ));
END;

CREATE TABLE transaction_conversions (
    transaction_id INTEGER PRIMARY KEY REFERENCES transactions(id),
    original_amount_minor INTEGER NOT NULL,
    original_currency TEXT NOT NULL CHECK (original_currency GLOB '[A-Z][A-Z][A-Z]'),
    original_minor_unit INTEGER NOT NULL CHECK (original_minor_unit BETWEEN 0 AND 4),
    eur_cents INTEGER NOT NULL,
    rate TEXT NOT NULL CHECK (
        length(rate) BETWEEN 1 AND 128 AND rate NOT GLOB '*[^0-9.]*' AND
        substr(rate, 1, 1) GLOB '[0-9]' AND substr(rate, -1, 1) GLOB '[0-9]' AND
        length(rate) - length(replace(rate, '.', '')) <= 1 AND
        replace(replace(rate, '0', ''), '.', '') != ''
    ),
    rate_date TEXT NOT NULL CHECK (length(rate_date) = 10),
    provider_policy TEXT NOT NULL,
    rounding_rule TEXT NOT NULL CHECK (rounding_rule = 'half_away_from_zero'),
    converted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;
CREATE TRIGGER transaction_conversions_no_update BEFORE UPDATE ON transaction_conversions
BEGIN
    SELECT RAISE(ABORT, 'saved conversion details are immutable');
END;
CREATE TRIGGER transaction_conversions_no_delete BEFORE DELETE ON transaction_conversions
BEGIN
    SELECT RAISE(ABORT, 'saved conversion details are immutable');
END;
