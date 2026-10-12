-- Transactions whose category TypeSafe Jev picked at import. Confirming the
-- batch turns the kept or corrected category into a payee rule. The category
-- is read from the transaction, so merging categories needs no change here.
CREATE TABLE category_suggestions (
    transaction_id INTEGER PRIMARY KEY REFERENCES transactions(id),
    confidence REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1)
) STRICT;
