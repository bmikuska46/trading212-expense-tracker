-- A payee has at most one categorization rule. A later correction replaces it.
CREATE UNIQUE INDEX categorization_rules_one_per_payee ON categorization_rules(payee_id) WHERE payee_id IS NOT NULL;
CREATE INDEX categorization_rules_category ON categorization_rules(category_id);
CREATE INDEX transaction_splits_payee ON transaction_splits(payee_id) WHERE payee_id IS NOT NULL;

-- A normalized name resolves to exactly one entity, either as its primary
-- name or as an alias left behind by a rename or merge.
CREATE TRIGGER categories_name_not_alias_insert BEFORE INSERT ON categories
WHEN EXISTS (SELECT 1 FROM category_aliases WHERE normalized_name = NEW.normalized_name)
BEGIN
    SELECT RAISE(ABORT, 'category name is already an alias');
END;
CREATE TRIGGER categories_name_not_alias_update BEFORE UPDATE OF normalized_name ON categories
WHEN EXISTS (SELECT 1 FROM category_aliases WHERE normalized_name = NEW.normalized_name)
BEGIN
    SELECT RAISE(ABORT, 'category name is already an alias');
END;
CREATE TRIGGER category_aliases_not_name BEFORE INSERT ON category_aliases
WHEN EXISTS (SELECT 1 FROM categories WHERE normalized_name = NEW.normalized_name)
BEGIN
    SELECT RAISE(ABORT, 'category alias matches a category name');
END;
CREATE TRIGGER payees_name_not_alias_insert BEFORE INSERT ON payees
WHEN EXISTS (SELECT 1 FROM payee_aliases WHERE normalized_name = NEW.normalized_name)
BEGIN
    SELECT RAISE(ABORT, 'payee name is already an alias');
END;
CREATE TRIGGER payees_name_not_alias_update BEFORE UPDATE OF normalized_name ON payees
WHEN EXISTS (SELECT 1 FROM payee_aliases WHERE normalized_name = NEW.normalized_name)
BEGIN
    SELECT RAISE(ABORT, 'payee name is already an alias');
END;
CREATE TRIGGER payee_aliases_not_name BEFORE INSERT ON payee_aliases
WHEN EXISTS (SELECT 1 FROM payees WHERE normalized_name = NEW.normalized_name)
BEGIN
    SELECT RAISE(ABORT, 'payee alias matches a payee name');
END;

-- Uncategorized is the fallback for every import and cannot be renamed away.
CREATE TRIGGER categories_uncategorized_rename BEFORE UPDATE OF normalized_name ON categories
WHEN OLD.normalized_name = 'uncategorized' AND NEW.normalized_name != 'uncategorized'
BEGIN
    SELECT RAISE(ABORT, 'Uncategorized cannot be renamed');
END;
CREATE TRIGGER categories_uncategorized_delete BEFORE DELETE ON categories
WHEN OLD.normalized_name = 'uncategorized'
BEGIN
    SELECT RAISE(ABORT, 'Uncategorized cannot be removed');
END;

-- Splits are allowed only on expenses and income.
CREATE TRIGGER transaction_splits_kind BEFORE INSERT ON transaction_splits
WHEN (SELECT kind FROM transactions WHERE id = NEW.transaction_id) NOT IN ('expense', 'income')
BEGIN
    SELECT RAISE(ABORT, 'only expenses and income can be split');
END;
