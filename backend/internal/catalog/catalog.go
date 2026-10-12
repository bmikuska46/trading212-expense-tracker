// Package catalog resolves, renames, and merges payees and categories, and
// applies local categorization rules. Every function runs on the caller's
// transaction so entity changes commit together with the ledger rows that
// use them.
package catalog

import (
	"context"
	"database/sql"
	"errors"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

// Kind selects the category or payee tables. The table and column names come
// only from these two constants, never from request input.
type Kind struct {
	table, aliases, column, splitColumn string
}

var (
	Categories = Kind{table: "categories", aliases: "category_aliases", column: "category_id", splitColumn: "category_id"}
	Payees     = Kind{table: "payees", aliases: "payee_aliases", column: "payee_id", splitColumn: "payee_id"}
)

var (
	ErrInvalidName = errors.New("enter a name of up to 100 characters")
	ErrNameTaken   = errors.New("another entry already uses this name; merge them instead")
	ErrProtected   = errors.New("Uncategorized cannot be renamed or merged away")
	ErrNotFound    = errors.New("entry not found")
	ErrSameEntry   = errors.New("choose a different entry to merge into")
)

const uncategorized = "uncategorized"

// Find returns the entity for an equivalent name, checking aliases first so
// names left behind by renames and merges keep resolving to their target.
func (k Kind) Find(ctx context.Context, db dbgen.DBTX, name string) (int64, bool, error) {
	key := ledger.NormalizeName(name)
	if key == "" {
		return 0, false, ErrInvalidName
	}
	var id int64
	err := db.QueryRowContext(ctx, "SELECT "+k.column+" FROM "+k.aliases+" WHERE normalized_name = ?", key).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	err = db.QueryRowContext(ctx, "SELECT id FROM "+k.table+" WHERE normalized_name = ?", key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Resolve finds the entity for an equivalent name or creates it.
func (k Kind) Resolve(ctx context.Context, db dbgen.DBTX, name string) (id int64, created bool, err error) {
	display := ledger.DisplayName(name)
	if !ledger.ValidName(display) {
		return 0, false, ErrInvalidName
	}
	id, found, err := k.Find(ctx, db, display)
	if err != nil || found {
		return id, false, err
	}
	err = db.QueryRowContext(ctx, "INSERT INTO "+k.table+" (name, normalized_name) VALUES (?, ?) RETURNING id",
		display, ledger.NormalizeName(display)).Scan(&id)
	return id, err == nil, err
}

// Exists reports whether an entity with this ID exists.
func (k Kind) Exists(ctx context.Context, db dbgen.DBTX, id int64) (bool, error) {
	var found int64
	err := db.QueryRowContext(ctx, "SELECT id FROM "+k.table+" WHERE id = ?", id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (k Kind) normalizedName(ctx context.Context, db dbgen.DBTX, id int64) (string, error) {
	var key string
	err := db.QueryRowContext(ctx, "SELECT normalized_name FROM "+k.table+" WHERE id = ?", id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return key, err
}

// Rename changes the display name. When the normalized name changes, the old
// one becomes an alias so future source labels still resolve here.
func (k Kind) Rename(ctx context.Context, db dbgen.DBTX, id int64, name string) error {
	display := ledger.DisplayName(name)
	if !ledger.ValidName(display) {
		return ErrInvalidName
	}
	oldKey, err := k.normalizedName(ctx, db, id)
	if err != nil {
		return err
	}
	newKey := ledger.NormalizeName(display)
	if k == Categories && oldKey == uncategorized && newKey != uncategorized {
		return ErrProtected
	}
	if newKey != oldKey {
		owner, found, err := k.Find(ctx, db, display)
		if err != nil {
			return err
		}
		if found && owner != id {
			return ErrNameTaken
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM "+k.aliases+" WHERE normalized_name = ?", newKey); err != nil {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, "UPDATE "+k.table+" SET name = ?, normalized_name = ? WHERE id = ?", display, newKey, id); err != nil {
		return err
	}
	if newKey != oldKey {
		_, err = db.ExecContext(ctx, "INSERT INTO "+k.aliases+" (normalized_name, "+k.column+") VALUES (?, ?)", oldKey, id)
	}
	return err
}

// Merge moves every transaction, split, rule, and alias from source to
// target, then removes source and keeps its name as an alias of target.
// Transactions are revised before their splits change so edit history
// captures the previous split categories.
func (k Kind) Merge(ctx context.Context, db dbgen.DBTX, source, target int64) error {
	if source == target {
		return ErrSameEntry
	}
	sourceKey, err := k.normalizedName(ctx, db, source)
	if err != nil {
		return err
	}
	if _, err := k.normalizedName(ctx, db, target); err != nil {
		return err
	}
	if k == Categories && sourceKey == uncategorized {
		return ErrProtected
	}
	statements := []string{
		"UPDATE transactions SET revision = revision + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') " +
			"WHERE " + k.column + " = ?1 OR id IN (SELECT transaction_id FROM transaction_splits WHERE " + k.splitColumn + " = ?1)",
		"UPDATE transactions SET " + k.column + " = ?2 WHERE " + k.column + " = ?1",
		"UPDATE transaction_splits SET " + k.splitColumn + " = ?2 WHERE " + k.splitColumn + " = ?1",
		"UPDATE " + k.aliases + " SET " + k.column + " = ?2 WHERE " + k.column + " = ?1",
	}
	if k == Payees {
		// The target's own rule wins when both payees have one.
		statements = append(statements,
			"DELETE FROM categorization_rules WHERE payee_id = ?1 AND EXISTS (SELECT 1 FROM categorization_rules WHERE payee_id = ?2)",
			"UPDATE categorization_rules SET payee_id = ?2 WHERE payee_id = ?1")
	} else {
		statements = append(statements, "UPDATE categorization_rules SET category_id = ?2 WHERE category_id = ?1")
	}
	statements = append(statements, "DELETE FROM "+k.table+" WHERE id = ?1")
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement, source, target); err != nil {
			return err
		}
	}
	_, err = db.ExecContext(ctx, "INSERT INTO "+k.aliases+" (normalized_name, "+k.column+") VALUES (?, ?)", sourceKey, target)
	return err
}

// Aliases lists the normalized names that resolve to each entity.
func (k Kind) Aliases(ctx context.Context, db dbgen.DBTX) (map[int64][]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+k.column+", normalized_name FROM "+k.aliases+" ORDER BY normalized_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aliases := map[int64][]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		aliases[id] = append(aliases[id], name)
	}
	return aliases, rows.Err()
}

// Uncategorized returns the fallback category.
func Uncategorized(ctx context.Context, db dbgen.DBTX) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, "SELECT id FROM categories WHERE normalized_name = ?", uncategorized).Scan(&id)
	return id, err
}

// RuleCategory returns the category of the payee's enabled rule.
func RuleCategory(ctx context.Context, db dbgen.DBTX, payeeID int64) (int64, bool, error) {
	var id int64
	err := db.QueryRowContext(ctx, "SELECT category_id FROM categorization_rules WHERE payee_id = ? AND enabled = 1", payeeID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// HasRule reports whether the payee has a rule, enabled or not. A disabled
// rule means the owner chose to categorize the payee by hand.
func HasRule(ctx context.Context, db dbgen.DBTX, payeeID int64) (bool, error) {
	var found int64
	err := db.QueryRowContext(ctx, "SELECT 1 FROM categorization_rules WHERE payee_id = ?", payeeID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetRule creates or replaces the payee's rule and returns its ID. Existing
// transactions are not changed.
func SetRule(ctx context.Context, db dbgen.DBTX, payeeID, categoryID int64) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, "SELECT id FROM categorization_rules WHERE payee_id = ?", payeeID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = db.QueryRowContext(ctx, "INSERT INTO categorization_rules (payee_id, category_id) VALUES (?, ?) RETURNING id",
			payeeID, categoryID).Scan(&id)
		return id, err
	case err != nil:
		return 0, err
	}
	_, err = db.ExecContext(ctx, "UPDATE categorization_rules SET category_id = ?, enabled = 1 WHERE id = ?", categoryID, id)
	return id, err
}

// Categorize picks the category for a new transaction: the payee's rule,
// then the source label, then Uncategorized. A source label creates the
// category when no equivalent name exists.
func Categorize(ctx context.Context, db dbgen.DBTX, payeeID sql.NullInt64, sourceLabel string) (int64, error) {
	if payeeID.Valid {
		if id, found, err := RuleCategory(ctx, db, payeeID.Int64); err != nil || found {
			return id, err
		}
	}
	if label := ledger.LabelName(sourceLabel); ledger.ValidName(label) {
		id, _, err := Categories.Resolve(ctx, db, label)
		return id, err
	}
	return Uncategorized(ctx, db)
}
