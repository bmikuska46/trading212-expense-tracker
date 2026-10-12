package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"budget-tracking/backend/internal/catalog"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/integrations"
	"budget-tracking/backend/internal/ledger"
)

const (
	// categoryChunk is the most payees asked about in one request.
	categoryChunk = 50
	// categoryTimeout bounds every request for one commit together.
	categoryTimeout  = 90 * time.Second
	maxCategoryNotes = 200
	noCategory       = "none"
)

// directions describe a kind to Jev. Transfers are never categorized.
var directions = map[string]string{
	ledger.KindExpense: "money out",
	ledger.KindIncome:  "money in",
	ledger.KindRefund:  "money returned for an earlier purchase",
}

// pick is a category Jev chose for a payee with enough confidence.
type pick struct {
	category   int64
	confidence float64
}

// newPayee is what Jev learns about one payee: its name, the direction of
// its first transaction in the file, and that transaction's notes.
type newPayee struct {
	key, name, direction, notes string
}

type categoryOption struct {
	id   int64
	name string
}

// categoryAsk returns Jev's ask when a TypeSafe key is saved and category
// suggestions are on. The switch is separate from mapping suggestions.
func (p *Pipeline) categoryAsk(ctx context.Context) (Ask, bool, error) {
	if p.TypeSafe == nil {
		return nil, false, nil
	}
	settings := p.Settings
	if settings == nil {
		settings = p.store
	}
	row, err := settings.Queries.GetIntegration(ctx, "typesafe")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var stored struct {
		APIKey     string `json:"apiKey"`
		Categorize bool   `json:"categorize"`
	}
	if json.Unmarshal([]byte(row.CredentialsJson), &stored) != nil || stored.APIKey == "" || !stored.Categorize {
		return nil, false, nil
	}
	return func(ctx context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error) {
		return p.TypeSafe.Ask(ctx, stored.APIKey, state, questions)
	}, true, nil
}

// suggestCategories asks Jev to categorize the batch's payees that would
// otherwise land in Uncategorized, keyed by normalized payee name. Only
// payee names, directions, and notes are sent; amounts, dates, and accounts
// never leave the server. A failure only means fewer picks, so it is logged
// and the commit goes on.
func (p *Pipeline) suggestCategories(ctx context.Context, batchID int64) map[string]pick {
	ask, ok, err := p.categoryAsk(ctx)
	if err != nil {
		slog.Warn("category suggestions skipped: load typesafe integration", "batch", batchID)
		return nil
	}
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, categoryTimeout)
	defer cancel()
	payees, err := newPayees(ctx, p.store.Queries, p.store.DB, batchID)
	if err != nil {
		slog.Warn("category suggestions skipped: read staged payees", "batch", batchID)
		return nil
	}
	if len(payees) == 0 {
		return nil
	}
	options, err := categoryOptions(ctx, p.store.DB)
	if err != nil {
		slog.Warn("category suggestions skipped: list categories", "batch", batchID)
		return nil
	}
	picks, err := askCategories(ctx, ask, payees, options)
	if err != nil {
		message := "request failed"
		var known *integrations.SuggestError
		if errors.As(err, &known) {
			message = known.Message
		}
		slog.Warn("category suggestions incomplete: ask typesafe", "batch", batchID, "picks", len(picks), "reason", message)
	}
	return picks
}

// newPayees returns the distinct payees of valid rows that no rule and no
// source category label would categorize.
func newPayees(ctx context.Context, q *dbgen.Queries, db dbgen.DBTX, batchID int64) ([]newPayee, error) {
	var payees []newPayee
	seen := map[string]bool{}
	var after int64
	for {
		page, err := q.ListStagingRows(ctx, dbgen.ListStagingRowsParams{BatchID: batchID, AfterRow: after, PageSize: chunkSize})
		if err != nil {
			return nil, err
		}
		for _, row := range page {
			if row.Status != RowValid || !row.ParsedRecordJson.Valid {
				continue
			}
			var record Record
			if err := json.Unmarshal([]byte(row.ParsedRecordJson.String), &record); err != nil {
				return nil, err
			}
			direction, categorized := directions[record.Kind]
			name := ledger.DisplayName(record.Payee)
			if !categorized || !ledger.ValidName(name) || ledger.ValidName(ledger.LabelName(record.Category)) {
				continue
			}
			key := ledger.NormalizeName(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			id, found, err := catalog.Payees.Find(ctx, db, name)
			if err != nil {
				return nil, err
			}
			if found {
				if ruled, err := catalog.HasRule(ctx, db, id); err != nil || ruled {
					if err != nil {
						return nil, err
					}
					continue
				}
			}
			payees = append(payees, newPayee{key: key, name: name, direction: direction, notes: truncate(record.Notes, maxCategoryNotes)})
		}
		if len(page) < chunkSize {
			return payees, nil
		}
		after = page[len(page)-1].RowNumber
	}
}

// categoryOptions lists the owner's categories other than Uncategorized.
func categoryOptions(ctx context.Context, q dbgen.DBTX) ([]categoryOption, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name FROM categories WHERE normalized_name != 'uncategorized' ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var options []categoryOption
	for rows.Next() {
		var option categoryOption
		if err := rows.Scan(&option.id, &option.name); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func categoryKey(index int) string { return "category " + strconv.Itoa(index+1) }

// askCategories asks one question per payee, in chunks, with the owner's
// categories and "none" as options; Jev cannot create a category. Picks
// below the confidence threshold are dropped, so the payee stays
// Uncategorized and is asked about again on the next import. The picks of
// chunks answered before an error are kept.
func askCategories(ctx context.Context, ask Ask, payees []newPayee, options []categoryOption) (map[string]pick, error) {
	if len(options) == 0 || len(options)+1 > maxChoiceOptions {
		return nil, nil
	}
	criteria := make(map[string]any, len(options)+1)
	names := make([]string, len(options))
	for index, option := range options {
		criteria[categoryKey(index)] = option.name
		names[index] = option.name
	}
	criteria[noCategory] = "None of these categories fits"
	state := map[string]any{"categories": names}
	picks := map[string]pick{}
	for start := 0; start < len(payees); start += categoryChunk {
		chunk := payees[start:min(start+categoryChunk, len(payees))]
		questions := make(map[string]integrations.Question, len(chunk))
		for index, payee := range chunk {
			instructions := map[string]any{
				"merchant":  payee.name,
				"direction": payee.direction,
				"question": "Which of the owner's categories fits a transaction with `merchant`? `direction` says whether money left " +
					"or came into the account, and `notes`, when present, is the bank's description of it.",
			}
			if payee.notes != "" {
				instructions["notes"] = payee.notes
			}
			questions["payee "+strconv.Itoa(index+1)] = choice(instructions, criteria)
		}
		answers, err := ask(ctx, state, questions)
		if err != nil {
			return picks, err
		}
		for index, payee := range chunk {
			answer := answers["payee "+strconv.Itoa(index+1)]
			number, found := strings.CutPrefix(answer.Choice, "category ")
			option, err := strconv.Atoi(number)
			if !found || err != nil || option < 1 || option > len(options) || answer.Confidence < confident {
				continue
			}
			picks[payee.key] = pick{category: options[option-1].id, confidence: min(max(answer.Confidence, 0), 1)}
		}
	}
	return picks, nil
}

// LedgerPayee is a payee of transactions already in the ledger that have no
// category yet, with the kind and latest notes of those transactions.
type LedgerPayee struct {
	Key, Name, Kind, Notes string
}

// CategorizeLedger asks Jev to categorize payees of existing transactions
// and returns the picked category IDs by key. Like at import, only names,
// directions, and notes are sent, and picks below the confidence threshold
// are dropped. The picks of chunks answered before an error are kept.
func CategorizeLedger(ctx context.Context, ask Ask, db dbgen.DBTX, payees []LedgerPayee) (map[string]int64, error) {
	options, err := categoryOptions(ctx, db)
	if err != nil {
		return nil, err
	}
	questions := make([]newPayee, 0, len(payees))
	for _, payee := range payees {
		if direction, ok := directions[payee.Kind]; ok {
			questions = append(questions, newPayee{key: payee.Key, name: payee.Name, direction: direction,
				notes: truncate(payee.Notes, maxCategoryNotes)})
		}
	}
	picks, err := askCategories(ctx, ask, questions, options)
	categories := make(map[string]int64, len(picks))
	for key, hint := range picks {
		categories[key] = hint.category
	}
	return categories, err
}

// applyPick replaces Uncategorized with Jev's pick for the payee, when the
// picked category still exists.
func applyPick(ctx context.Context, q txn, picks map[string]pick, record *Record, payee sql.NullInt64, category int64) (int64, *pick, error) {
	if !payee.Valid || len(picks) == 0 {
		return category, nil, nil
	}
	hint, ok := picks[ledger.NormalizeName(ledger.DisplayName(record.Payee))]
	if !ok {
		return category, nil, nil
	}
	fallback, err := catalog.Uncategorized(ctx, q)
	if err != nil || category != fallback {
		return category, nil, err
	}
	exists, err := catalog.Categories.Exists(ctx, q, hint.category)
	if err != nil || !exists {
		return category, nil, err
	}
	return hint.category, &hint, nil
}

func recordSuggestion(ctx context.Context, q txn, transactionID int64, hint *pick) error {
	_, err := q.ExecContext(ctx, "INSERT INTO category_suggestions (transaction_id, confidence) VALUES (?, ?)", transactionID, hint.confidence)
	return err
}

// SuggestedCount returns how many of the batch's transactions have a
// category Jev picked.
func SuggestedCount(ctx context.Context, db dbgen.DBTX, batchID int64) (int64, error) {
	var count int64
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM category_suggestions s
		JOIN transactions t ON t.id = s.transaction_id
		WHERE t.import_batch_id = ? AND t.deleted_at IS NULL`, batchID).Scan(&count)
	return count, err
}

// LearnRules runs when the owner confirms a batch. Each payee whose
// suggested transactions all kept one category other than Uncategorized,
// as picked or as corrected, gets a rule for it, so the payee is never
// asked about again. Payees that have a rule by now, and split
// transactions, are left alone.
func LearnRules(ctx context.Context, db dbgen.DBTX, batchID int64) error {
	rows, err := db.QueryContext(ctx, `SELECT t.payee_id, t.category_id FROM category_suggestions s
		JOIN transactions t ON t.id = s.transaction_id
		WHERE t.import_batch_id = ? AND t.deleted_at IS NULL AND t.payee_id IS NOT NULL AND t.category_id IS NOT NULL
			AND t.kind != 'transfer'
			AND NOT EXISTS (SELECT 1 FROM transaction_splits WHERE transaction_id = t.id)
		ORDER BY t.payee_id`, batchID)
	if err != nil {
		return err
	}
	kept := map[int64]int64{}
	mixed := map[int64]bool{}
	var order []int64
	for rows.Next() {
		var payee, category int64
		if err := rows.Scan(&payee, &category); err != nil {
			rows.Close()
			return err
		}
		previous, seen := kept[payee]
		switch {
		case !seen:
			kept[payee] = category
			order = append(order, payee)
		case previous != category:
			mixed[payee] = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fallback, err := catalog.Uncategorized(ctx, db)
	if err != nil {
		return err
	}
	for _, payee := range order {
		if mixed[payee] || kept[payee] == fallback {
			continue
		}
		ruled, err := catalog.HasRule(ctx, db, payee)
		if err != nil {
			return err
		}
		if ruled {
			continue
		}
		if _, err := catalog.SetRule(ctx, db, payee, kept[payee]); err != nil {
			return err
		}
	}
	return nil
}
