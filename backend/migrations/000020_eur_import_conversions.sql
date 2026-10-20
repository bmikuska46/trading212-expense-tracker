DROP TRIGGER transaction_conversions_match;
CREATE TRIGGER transaction_conversions_match BEFORE INSERT ON transaction_conversions
WHEN NOT EXISTS (
    SELECT 1 FROM transactions WHERE id = NEW.transaction_id AND eur_cents = NEW.eur_cents AND (
        (amount_minor = NEW.original_amount_minor AND currency = NEW.original_currency AND minor_unit = NEW.original_minor_unit) OR
        (currency = 'EUR' AND minor_unit = 2 AND amount_minor = NEW.eur_cents AND NEW.original_currency != 'EUR')
    )
)
BEGIN
    SELECT RAISE(ABORT, 'conversion details must match the transaction amount');
END;
