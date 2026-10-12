-- confirmed_through is the last local date on which a fetch showed this row
-- was the latest publication for its currency. Lookups use a rate only from
-- its publication date through that day, so days the server missed are never
-- assigned a rate that a later fetch returned.
ALTER TABLE exchange_rates ADD COLUMN confirmed_through TEXT CHECK (
    confirmed_through IS NULL OR (length(confirmed_through) = 10 AND confirmed_through >= publication_date)
);
UPDATE exchange_rates SET confirmed_through = publication_date;
