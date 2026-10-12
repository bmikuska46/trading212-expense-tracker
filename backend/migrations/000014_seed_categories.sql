-- Basic categories give Jev something to choose from on a new ledger. A name
-- the owner already uses, directly or as an alias, is left alone.
INSERT INTO categories (name, normalized_name)
SELECT column1, lower(column1) FROM (VALUES
    ('Groceries'), ('Eating out'), ('Transport'), ('Housing'), ('Utilities'),
    ('Health'), ('Shopping'), ('Entertainment'), ('Subscriptions'), ('Travel'),
    ('Personal care'), ('Education'), ('Gifts and donations'), ('Fees'),
    ('Salary'), ('Interest'), ('Other income')
)
WHERE lower(column1) NOT IN (SELECT normalized_name FROM categories)
    AND lower(column1) NOT IN (SELECT normalized_name FROM category_aliases);
