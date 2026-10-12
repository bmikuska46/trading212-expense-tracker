-- Investment income keeps dividends, interest, and cashback from Trading 212
-- for the tax return. amount_minor is in the currency the income was issued
-- in: the gross dividend in the instrument currency, and interest and
-- cashback as credited. received_minor is what reached the account, after
-- any withholding tax. The informative EUR value uses the latest cached rate
-- when the row was saved and is never used for tax.
CREATE TABLE investment_income (
    id INTEGER PRIMARY KEY,
    source_id TEXT NOT NULL UNIQUE CHECK (length(source_id) > 0),
    kind TEXT NOT NULL CHECK (kind IN ('dividend', 'interest', 'cashback')),
    paid_at TEXT NOT NULL,
    paid_on TEXT NOT NULL CHECK (paid_on = date(paid_on)),
    currency TEXT NOT NULL CHECK (currency GLOB '[A-Z][A-Z][A-Z]'),
    amount_minor INTEGER NOT NULL,
    source_type TEXT NOT NULL,
    ticker TEXT,
    isin TEXT,
    name TEXT,
    quantity TEXT,
    per_share TEXT,
    received_currency TEXT NOT NULL CHECK (received_currency GLOB '[A-Z][A-Z][A-Z]'),
    received_minor INTEGER NOT NULL,
    informative_eur_cents INTEGER,
    informative_rate TEXT,
    informative_rate_date TEXT,
    imported_at TEXT NOT NULL,
    CHECK ((informative_eur_cents IS NULL) = (informative_rate IS NULL))
) STRICT;
CREATE INDEX investment_income_paid_on ON investment_income(paid_on);

-- NBS rates are foreign units per EUR as published by Národná banka
-- Slovenska. period is a day (YYYY-MM-DD), the official average of a
-- finished month (YYYY-MM), or the official average of a finished year
-- (YYYY).
CREATE TABLE nbs_rates (
    currency TEXT NOT NULL CHECK (currency GLOB '[A-Z][A-Z][A-Z]'),
    period TEXT NOT NULL CHECK (length(period) IN (4, 7, 10)),
    rate TEXT NOT NULL CHECK (length(rate) > 0),
    fetched_at TEXT NOT NULL,
    PRIMARY KEY (currency, period)
) STRICT;

-- A month or year file is complete once it was read after the period ended
-- and holds its official average, so it is never downloaded again.
CREATE TABLE nbs_rate_files (
    period TEXT PRIMARY KEY CHECK (length(period) IN (4, 7)),
    fetched_at TEXT NOT NULL,
    complete INTEGER NOT NULL CHECK (complete IN (0, 1))
) STRICT;

-- started_on is the first day imported, the 1st of January of the year of
-- the first sync. Dividends and interest come from paged history, each
-- synced on its own; cashback comes from a transactions report covering up
-- to cashback_through. The report columns track the report the job waits
-- for; report_requested_at and report_polled_at also keep the Trading 212
-- report limits across restarts. Each *_next_at is when that part runs
-- again; a saved key makes history and cashback due at once.
CREATE TABLE investment_income_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    started_on TEXT CHECK (started_on IS NULL OR started_on = date(started_on)),
    dividends_synced_at TEXT,
    interest_synced_at TEXT,
    history_attempt_at TEXT,
    history_error TEXT,
    history_next_at TEXT,
    cashback_through TEXT,
    cashback_attempt_at TEXT,
    cashback_error TEXT,
    cashback_next_at TEXT,
    report_id TEXT,
    report_from TEXT,
    report_to TEXT,
    report_requested_at TEXT,
    report_polled_at TEXT,
    rates_checked_at TEXT,
    rates_error TEXT,
    rates_next_at TEXT
) STRICT;
INSERT INTO investment_income_state (id) VALUES (1);
