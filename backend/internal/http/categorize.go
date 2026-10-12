package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"budget-tracking/backend/internal/catalog"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/imports"
	"budget-tracking/backend/internal/integrations"
)

const categorizeTimeout = 2 * time.Minute

// pendingTransaction is a ledger transaction without a category of its own.
type pendingTransaction struct {
	id, payeeID       int64
	payee, kind, note string
}

func (p pendingTransaction) key() string { return strconv.FormatInt(p.payeeID, 10) + " " + p.kind }

// listPending returns live transactions with a payee that are Uncategorized
// or have no category, newest first. Transfers, splits, and linked refunds
// get their categories elsewhere, so they are left out.
func listPending(ctx context.Context, db dbgen.DBTX, fallback int64) ([]pendingTransaction, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.id, t.payee_id, p.name, t.kind, t.notes FROM transactions t
		JOIN payees p ON p.id = t.payee_id
		WHERE t.deleted_at IS NULL AND t.kind != 'transfer' AND t.refund_of_transaction_id IS NULL
			AND (t.category_id IS NULL OR t.category_id = ?)
			AND NOT EXISTS (SELECT 1 FROM transaction_splits WHERE transaction_id = t.id)
		ORDER BY t.local_date DESC, t.id DESC`, fallback)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []pendingTransaction
	for rows.Next() {
		var item pendingTransaction
		if err := rows.Scan(&item.id, &item.payeeID, &item.payee, &item.kind, &item.note); err != nil {
			return nil, err
		}
		pending = append(pending, item)
	}
	return pending, rows.Err()
}

// categorizeTransactions gives every Uncategorized transaction a category.
// A payee's rule decides first; Jev picks for the remaining payees, once per
// payee and kind. Transactions Jev is not sure about stay Uncategorized.
func (a *api) categorizeTransactions(w http.ResponseWriter, r *http.Request, _ *session) {
	ctx := r.Context()
	apiKey, ready, err := a.typeSafeReady(ctx)
	if err != nil {
		internalError(w, "load typesafe integration")
		return
	}
	if !ready {
		writeError(w, http.StatusConflict, "Connect and enable TypeSafe in Settings to categorize with Jev")
		return
	}
	fallback, err := catalog.Uncategorized(ctx, a.store.DB)
	if err != nil {
		internalError(w, "load uncategorized")
		return
	}
	pending, err := listPending(ctx, a.store.DB, fallback)
	if err != nil {
		internalError(w, "list uncategorized transactions")
		return
	}

	// Each payee and kind is asked about once, with its latest notes.
	ruled := map[int64]int64{}
	var asked []imports.LedgerPayee
	seen := map[string]int{}
	for _, item := range pending {
		if _, done := ruled[item.payeeID]; done {
			continue
		}
		if index, found := seen[item.key()]; found {
			if asked[index].Notes == "" {
				asked[index].Notes = item.note
			}
			continue
		}
		category, found, err := catalog.RuleCategory(ctx, a.store.DB, item.payeeID)
		if err != nil {
			internalError(w, "load payee rule")
			return
		}
		if found {
			ruled[item.payeeID] = category
			continue
		}
		seen[item.key()] = len(asked)
		asked = append(asked, imports.LedgerPayee{Key: item.key(), Name: item.payee, Kind: item.kind, Notes: item.note})
	}

	var picks map[string]int64
	if len(asked) > 0 {
		extendDeadlines(w, 0, categorizeTimeout+10*time.Second)
		askCtx, cancel := context.WithTimeout(ctx, categorizeTimeout)
		defer cancel()
		ask := func(ctx context.Context, state any, questions map[string]integrations.Question) (map[string]integrations.Answer, error) {
			return a.integrations.Ask(ctx, apiKey, state, questions)
		}
		picks, err = imports.CategorizeLedger(askCtx, ask, a.store.DB, asked)
		if err != nil {
			reason := "request failed"
			var known *integrations.SuggestError
			if errors.As(err, &known) {
				reason = known.Message
			}
			slog.Warn("ledger categorization incomplete: ask typesafe", "picks", len(picks), "reason", reason)
			if len(picks) == 0 && len(ruled) == 0 {
				writeError(w, http.StatusBadGateway, "Jev could not categorize the transactions. Check the TypeSafe integration or try again later.")
				return
			}
		}
	}

	changed := 0
	err = a.inTx(ctx, func(q txn) error {
		for _, item := range pending {
			category, ok := ruled[item.payeeID]
			if !ok {
				if category, ok = picks[item.key()]; !ok {
					continue
				}
			}
			// The owner may have edited the transaction while Jev answered.
			existing, err := q.GetTransaction(ctx, item.id)
			if err != nil {
				return err
			}
			if existing.DeletedAt.Valid || existing.Kind != item.kind || existing.RefundOfTransactionID.Valid ||
				!existing.PayeeID.Valid || existing.PayeeID.Int64 != item.payeeID ||
				(existing.CategoryID.Valid && existing.CategoryID.Int64 != fallback) {
				continue
			}
			splits, err := q.ListSplits(ctx, existing.ID)
			if err != nil {
				return err
			}
			if len(splits) > 0 {
				continue
			}
			existing.CategoryID = sql.NullInt64{Int64: category, Valid: true}
			if err := q.SetTransactionCategory(ctx, dbgen.SetTransactionCategoryParams{CategoryID: existing.CategoryID, ID: existing.ID}); err != nil {
				return err
			}
			if err := cascadeRefunds(ctx, q, existing); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	if err != nil {
		internalError(w, "categorize transactions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"categorized": changed, "remaining": len(pending) - changed})
}
