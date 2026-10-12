-- Rates are fetched with every account currency as the base, not only EUR,
-- so a cached rate is keyed by its base as well. Ledger conversions still
-- read the EUR-base rows.
CREATE TABLE exchange_rates_new (
    base_currency TEXT NOT NULL DEFAULT 'EUR' CHECK (base_currency GLOB '[A-Z][A-Z][A-Z]'),
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
    confirmed_through TEXT CHECK (
        confirmed_through IS NULL OR (length(confirmed_through) = 10 AND confirmed_through >= publication_date)
    ),
    PRIMARY KEY (base_currency, quote_currency, publication_date)
) STRICT;
INSERT INTO exchange_rates_new SELECT base_currency, quote_currency, publication_date, rate, provider_policy,
    fetched_at, confirmed_through FROM exchange_rates;
DROP TABLE exchange_rates;
ALTER TABLE exchange_rates_new RENAME TO exchange_rates;

-- The tax percentages the income page applies to each kind, which the user
-- can change in the settings. The defaults are the rates for a Slovak
-- resident: 7 % on dividends from abroad (§ 15 písm. a) bod 5 with § 51e
-- ods. 3), 19 % on interest in the special base of § 7 (bod 4), and at least
-- 19 % on cashback as other income under § 8, which joins the progressive
-- general base (bod 1, 19 % to 35 % from 2026). Dividends paid from 2024
-- profits are taxed at 10 % and those from a non-cooperating state at 35 %.
ALTER TABLE owner ADD COLUMN dividend_tax_percent INTEGER NOT NULL DEFAULT 7 CHECK (dividend_tax_percent BETWEEN 0 AND 100);
ALTER TABLE owner ADD COLUMN interest_tax_percent INTEGER NOT NULL DEFAULT 19 CHECK (interest_tax_percent BETWEEN 0 AND 100);
ALTER TABLE owner ADD COLUMN cashback_tax_percent INTEGER NOT NULL DEFAULT 19 CHECK (cashback_tax_percent BETWEEN 0 AND 100);
