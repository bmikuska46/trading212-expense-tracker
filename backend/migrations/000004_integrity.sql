-- Scheduled jobs can fail before they create an import batch, so failure
-- notifications may reference a job instead. Rebuild the child table first so
-- dropping the old parent never touches rows that still reference it.
CREATE TABLE notification_events_new (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER REFERENCES import_batches(id),
    job_id INTEGER REFERENCES scheduled_jobs(id),
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
    CHECK ((kind = 'import_review' AND batch_id IS NOT NULL) OR
           (kind = 'import_failure' AND (batch_id IS NOT NULL OR job_id IS NOT NULL)))
) STRICT;
INSERT INTO notification_events_new (
    id, batch_id, job_id, kind, payload_json, idempotency_key, delivery_state, attempt_count,
    next_attempt_at, first_attempt_at, email_id, last_error_code, last_error_detail, dismissed_at, created_at
) SELECT
    id, batch_id, NULL, kind, payload_json, idempotency_key, delivery_state, attempt_count,
    next_attempt_at, first_attempt_at, email_id, last_error_code, last_error_detail, dismissed_at, created_at
FROM notification_events;

CREATE TABLE notification_attempts_new (
    id INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES notification_events_new(id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    outcome TEXT CHECK (outcome IN ('delivered', 'transient_failure', 'permanent_failure', 'uncertain')),
    email_id TEXT,
    error_code TEXT,
    error_detail TEXT,
    UNIQUE (event_id, attempt_number)
) STRICT;
INSERT INTO notification_attempts_new SELECT * FROM notification_attempts;

DROP TABLE notification_attempts;
DROP TABLE notification_events;
-- Renaming also rewrites the REFERENCES clause in notification_attempts_new.
ALTER TABLE notification_events_new RENAME TO notification_events;
ALTER TABLE notification_attempts_new RENAME TO notification_attempts;
CREATE UNIQUE INDEX notification_events_batch_kind ON notification_events(kind, batch_id) WHERE batch_id IS NOT NULL;
CREATE UNIQUE INDEX notification_events_job_kind ON notification_events(kind, job_id) WHERE job_id IS NOT NULL;
CREATE INDEX notification_events_due ON notification_events(delivery_state, next_attempt_at);
CREATE INDEX notification_events_active ON notification_events(dismissed_at, batch_id);

-- Saved conversions are permanent, so the transaction amounts they describe
-- cannot drift away from them through owner edits.
CREATE TRIGGER transaction_conversions_match BEFORE INSERT ON transaction_conversions
WHEN NOT EXISTS (
    SELECT 1 FROM transactions WHERE id = NEW.transaction_id AND amount_minor = NEW.original_amount_minor AND
        currency = NEW.original_currency AND minor_unit = NEW.original_minor_unit AND eur_cents = NEW.eur_cents
)
BEGIN
    SELECT RAISE(ABORT, 'conversion details must match the transaction amount');
END;
CREATE TRIGGER transactions_converted_amount_locked BEFORE UPDATE OF amount_minor, currency, minor_unit, eur_cents ON transactions
WHEN (OLD.amount_minor != NEW.amount_minor OR OLD.currency != NEW.currency OR
      OLD.minor_unit != NEW.minor_unit OR OLD.eur_cents != NEW.eur_cents) AND
     EXISTS (SELECT 1 FROM transaction_conversions WHERE transaction_id = OLD.id)
BEGIN
    SELECT RAISE(ABORT, 'converted transaction amounts are locked');
END;

-- Transactions are deleted by setting deleted_at. Deleted imported rows keep
-- their source IDs, so a reimport of the same record is still detected as a
-- duplicate instead of reappearing, and edit history stays attached.
ALTER TABLE transactions ADD COLUMN deleted_at TEXT;
CREATE INDEX transactions_deleted ON transactions(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TRIGGER transactions_no_delete BEFORE DELETE ON transactions
BEGIN
    SELECT RAISE(ABORT, 'transactions are deleted by setting deleted_at');
END;
DROP TRIGGER transactions_capture_edit;
CREATE TRIGGER transactions_capture_edit BEFORE UPDATE ON transactions
BEGIN
    INSERT INTO transaction_edit_history (transaction_id, revision, snapshot_json)
    VALUES (OLD.id, OLD.revision, json_object(
        'account_id', OLD.account_id, 'occurred_at', OLD.occurred_at, 'local_date', OLD.local_date,
        'kind', OLD.kind, 'amount_minor', OLD.amount_minor, 'currency', OLD.currency,
        'minor_unit', OLD.minor_unit, 'eur_cents', OLD.eur_cents, 'payee_id', OLD.payee_id,
        'category_id', OLD.category_id, 'refund_of_transaction_id', OLD.refund_of_transaction_id,
        'notes', OLD.notes, 'review_status', OLD.review_status, 'deleted_at', OLD.deleted_at,
        'splits', json((SELECT coalesce(json_group_array(json_object(
            'position', position, 'category_id', category_id, 'payee_id', payee_id,
            'amount_minor', amount_minor, 'eur_cents', eur_cents, 'notes', notes
        )), '[]') FROM transaction_splits WHERE transaction_id = OLD.id))
    ));
END;
