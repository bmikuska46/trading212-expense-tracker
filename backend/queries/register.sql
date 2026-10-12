-- name: ListSplits :many
SELECT transaction_splits.*, categories.name AS category_name
FROM transaction_splits JOIN categories ON categories.id = transaction_splits.category_id
WHERE transaction_id = ? ORDER BY position;

-- name: DeleteSplits :exec
DELETE FROM transaction_splits WHERE transaction_id = ?;

-- name: CreateSplit :exec
INSERT INTO transaction_splits (transaction_id, position, category_id, payee_id, amount_minor, eur_cents, notes)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: CreateLinkedTransfer :one
INSERT INTO linked_transfers (id) VALUES (NULL) RETURNING id;

-- name: CreateTransferLeg :exec
INSERT INTO transfer_legs (transfer_id, transaction_id, direction) VALUES (?, ?, ?);

-- name: GetTransferCounterpart :one
SELECT other.transaction_id, other.direction
FROM transfer_legs AS own JOIN transfer_legs AS other
    ON other.transfer_id = own.transfer_id AND other.transaction_id != own.transaction_id
WHERE own.transaction_id = ?;

-- name: ListRefunds :many
SELECT * FROM transactions WHERE refund_of_transaction_id = ? AND deleted_at IS NULL ORDER BY local_date, id;

-- name: CountActiveRefunds :one
SELECT count(*) FROM transactions WHERE refund_of_transaction_id = ? AND deleted_at IS NULL;

-- name: SetTransactionCategory :exec
UPDATE transactions SET category_id = ?, revision = revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: SetTransactionReviewStatus :one
UPDATE transactions SET review_status = ?, revision = revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND revision = sqlc.arg(expected_revision) AND deleted_at IS NULL RETURNING *;

-- name: FlagSourceConflict :exec
UPDATE transactions SET review_status = 'source_conflict', revision = revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: ListCategoryRows :many
SELECT categories.id, categories.name, categories.normalized_name,
    CAST((SELECT count(*) FROM transactions WHERE transactions.category_id = categories.id AND transactions.deleted_at IS NULL) +
    (SELECT count(*) FROM transaction_splits JOIN transactions ON transactions.id = transaction_splits.transaction_id
        WHERE transaction_splits.category_id = categories.id AND transactions.deleted_at IS NULL) AS INTEGER) AS transaction_count
FROM categories ORDER BY categories.normalized_name = 'uncategorized' DESC, categories.name COLLATE NOCASE, categories.id;

-- name: CountPayees :one
SELECT count(*) FROM payees WHERE sqlc.arg(search) = '' OR instr(normalized_name, sqlc.arg(search)) > 0;

-- ListPayeeRows loads one page of payees in any order; the handler picks and
-- sorts the page.
-- name: ListPayeeRows :many
SELECT payees.id, payees.name, payees.normalized_name,
    CAST((SELECT count(*) FROM transactions WHERE transactions.payee_id = payees.id AND transactions.deleted_at IS NULL) AS INTEGER) AS transaction_count,
    rule.id AS rule_id, rule.category_id AS rule_category_id, rule_category.name AS rule_category_name
FROM payees LEFT JOIN categorization_rules AS rule ON rule.payee_id = payees.id
LEFT JOIN categories AS rule_category ON rule_category.id = rule.category_id
WHERE payees.id IN (sqlc.slice(ids));

-- name: GetPayee :one
SELECT * FROM payees WHERE id = ?;

-- name: GetCategory :one
SELECT * FROM categories WHERE id = ?;

-- name: ListRules :many
SELECT categorization_rules.id, categorization_rules.payee_id, payees.name AS payee_name,
    categorization_rules.category_id, categories.name AS category_name, categorization_rules.enabled
FROM categorization_rules
JOIN payees ON payees.id = categorization_rules.payee_id
JOIN categories ON categories.id = categorization_rules.category_id
ORDER BY payees.name COLLATE NOCASE, categorization_rules.id;

-- name: GetRule :one
SELECT * FROM categorization_rules WHERE id = ?;

-- name: UpdateRule :exec
UPDATE categorization_rules SET category_id = ?, enabled = ? WHERE id = ?;

-- name: DeleteRule :exec
DELETE FROM categorization_rules WHERE id = ?;
