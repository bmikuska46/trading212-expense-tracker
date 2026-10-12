-- Investing keeps hourly snapshots of the Trading 212 account and its pies.
-- Amounts are minor units of the account currency, rounded half away from
-- zero from the decimals Trading 212 reports.
CREATE TABLE investing_snapshots (
    id INTEGER PRIMARY KEY,
    fetched_at TEXT NOT NULL UNIQUE,
    account_id TEXT NOT NULL CHECK (length(account_id) > 0),
    currency TEXT NOT NULL CHECK (currency GLOB '[A-Z][A-Z][A-Z]'),
    total_value_minor INTEGER NOT NULL,
    investments_value_minor INTEGER NOT NULL,
    cost_basis_minor INTEGER NOT NULL,
    unrealized_minor INTEGER NOT NULL,
    realized_minor INTEGER NOT NULL,
    cash_available_minor INTEGER NOT NULL,
    cash_in_pies_minor INTEGER NOT NULL,
    cash_reserved_minor INTEGER NOT NULL
) STRICT;

-- Pie names come from a separate, heavily rate limited endpoint, so they are
-- cached and refreshed once a day. A pie stays listed after it is deleted in
-- Trading 212, which keeps its history and chart color.
CREATE TABLE investing_pies (
    id INTEGER PRIMARY KEY,
    name TEXT,
    name_checked_at TEXT
) STRICT;

CREATE TABLE investing_pie_snapshots (
    snapshot_id INTEGER NOT NULL REFERENCES investing_snapshots(id) ON DELETE CASCADE,
    pie_id INTEGER NOT NULL REFERENCES investing_pies(id),
    invested_minor INTEGER NOT NULL,
    value_minor INTEGER NOT NULL,
    result_minor INTEGER NOT NULL,
    cash_minor INTEGER NOT NULL,
    dividends_gained_minor INTEGER NOT NULL,
    dividends_reinvested_minor INTEGER NOT NULL,
    dividends_in_cash_minor INTEGER NOT NULL,
    PRIMARY KEY (snapshot_id, pie_id)
) STRICT;
CREATE INDEX investing_pie_snapshots_pie ON investing_pie_snapshots(pie_id, snapshot_id);

-- Groups gather pies so they can be shown and compared together.
CREATE TABLE investing_groups (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    normalized_name TEXT NOT NULL UNIQUE CHECK (length(normalized_name) > 0)
) STRICT;

CREATE TABLE investing_group_pies (
    group_id INTEGER NOT NULL REFERENCES investing_groups(id) ON DELETE CASCADE,
    pie_id INTEGER NOT NULL REFERENCES investing_pies(id),
    PRIMARY KEY (group_id, pie_id)
) STRICT;

-- started_on dates the first deposit, for the average yearly return. The
-- last attempt explains why snapshots stopped, such as a missing permission.
CREATE TABLE investing_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    started_on TEXT CHECK (started_on IS NULL OR started_on = date(started_on)),
    last_attempt_at TEXT,
    last_error TEXT
) STRICT;
INSERT INTO investing_state (id) VALUES (1);
