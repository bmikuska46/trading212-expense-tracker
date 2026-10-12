package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const (
	dashboardCategories = 8
	dashboardMonths     = 12
	dashboardReviews    = 5
)

// dashboardCategory is one bar of the spending breakdown. The remainder
// after the largest categories folds into a single Other line, so the lines
// always add up to the month's spending.
type dashboardCategory struct {
	CategoryID       *int64 `json:"categoryId"`
	CategoryName     string `json:"categoryName"`
	SpendingEURCents int64  `json:"spendingEurCents"`
	Other            bool   `json:"other"`
}

type trendMonth struct {
	Month            string `json:"month"`
	IncomeEURCents   int64  `json:"incomeEurCents"`
	SpendingEURCents int64  `json:"spendingEurCents"`
	NetEURCents      int64  `json:"netEurCents"`
}

// dashboardAccount keeps the source currency next to the EUR reporting
// value, so account activity can be reconciled against bank statements.
type dashboardAccount struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Currency         string `json:"currency"`
	Archived         bool   `json:"archived"`
	Balance          string `json:"balance"`
	BalanceEURCents  int64  `json:"balanceEurCents"`
	Change           string `json:"change"`
	ChangeEURCents   int64  `json:"changeEurCents"`
	TransactionCount int64  `json:"transactionCount"`
}

type reviewBatch struct {
	ID            int64      `json:"id"`
	Filename      string     `json:"filename"`
	Account       accountRef `json:"account"`
	InsertedCount int64      `json:"insertedCount"`
	PeriodStart   *string    `json:"periodStart"`
	PeriodEnd     *string    `json:"periodEnd"`
	CommittedAt   *string    `json:"committedAt"`
}

type dashboardReview struct {
	ImportCount      int64         `json:"importCount"`
	Imports          []reviewBatch `json:"imports"`
	NeedsReviewCount int64         `json:"needsReviewCount"`
	ConflictCount    int64         `json:"conflictCount"`
}

type dashboardView struct {
	Month            string              `json:"month"`
	From             string              `json:"from"`
	To               string              `json:"to"`
	AccountID        *int64              `json:"accountId"`
	IncomeEURCents   int64               `json:"incomeEurCents"`
	ExpenseEURCents  int64               `json:"expenseEurCents"`
	RefundEURCents   int64               `json:"refundEurCents"`
	SpendingEURCents int64               `json:"spendingEurCents"`
	NetEURCents      int64               `json:"netEurCents"`
	TransferCount    int64               `json:"transferCount"`
	Categories       []dashboardCategory `json:"categories"`
	Trend            []trendMonth        `json:"trend"`
	Accounts         []dashboardAccount  `json:"accounts"`
	Review           dashboardReview     `json:"review"`
}

// dashboard returns compact aggregates for one month. Totals come from the
// same query as the register summary, so they reconcile with a register
// filtered to the same month and account.
func (a *api) dashboard(w http.ResponseWriter, r *http.Request, current *session) {
	fields := map[string]string{}
	month := a.now().In(a.location(current))
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	if value := r.URL.Query().Get("month"); value != "" {
		parsed, err := time.Parse("2006-01", value)
		if err != nil || parsed.Year() < 1900 || parsed.Year() > 9999 {
			fields["month"] = "Use a month as YYYY-MM"
		}
		month = parsed
	}
	var accountID int64
	if value := r.URL.Query().Get("account"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err == nil && id <= 0 {
			err = sql.ErrNoRows
		}
		if err == nil {
			_, err = a.store.Queries.GetAccount(r.Context(), id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				internalError(w, "load account")
				return
			}
		}
		if err != nil {
			fields["account"] = "Choose an account"
		}
		accountID = id
	}
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	view, err := a.buildDashboard(r.Context(), month, accountID)
	if err != nil {
		internalError(w, "build dashboard")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) buildDashboard(ctx context.Context, month time.Time, accountID int64) (dashboardView, error) {
	from := month.Format(time.DateOnly)
	to := month.AddDate(0, 1, -1).Format(time.DateOnly)
	view := dashboardView{Month: month.Format("2006-01"), From: from, To: to}
	if accountID != 0 {
		view.AccountID = &accountID
	}
	stats, err := a.computeStatistics(ctx, registerFilter{from: from, to: to, accountID: accountID})
	if err != nil {
		return view, err
	}
	view.IncomeEURCents, view.ExpenseEURCents, view.RefundEURCents = stats.IncomeEURCents, stats.ExpenseEURCents, stats.RefundEURCents
	view.SpendingEURCents, view.NetEURCents, view.TransferCount = stats.SpendingEURCents, stats.NetEURCents, stats.TransferCount
	view.Categories = topCategories(stats)
	if view.Trend, err = a.trend(ctx, month, accountID); err != nil {
		return view, err
	}
	if view.Accounts, err = a.accountBalances(ctx, from, to, accountID); err != nil {
		return view, err
	}
	view.Review, err = a.pendingReview(ctx, accountID)
	return view, err
}

// topCategories keeps the categories with the most spending and folds the
// rest, including categories where refunds exceeded expenses, into Other.
func topCategories(stats statisticsView) []dashboardCategory {
	lines := []dashboardCategory{}
	remainder := stats.SpendingEURCents
	for _, total := range stats.Categories {
		if len(lines) == dashboardCategories || total.SpendingEURCents <= 0 {
			break
		}
		name := total.CategoryName
		if total.CategoryID == nil {
			name = "Uncategorized"
		}
		lines = append(lines, dashboardCategory{CategoryID: total.CategoryID, CategoryName: name, SpendingEURCents: total.SpendingEURCents})
		remainder -= total.SpendingEURCents
	}
	if remainder != 0 {
		lines = append(lines, dashboardCategory{CategoryName: "Other categories", SpendingEURCents: remainder, Other: true})
	}
	return lines
}

// trend totals the twelve months ending with the selected month in one
// grouped query over the same filter clause as the register.
func (a *api) trend(ctx context.Context, month time.Time, accountID int64) ([]trendMonth, error) {
	first := month.AddDate(0, 1-dashboardMonths, 0)
	filter := registerFilter{from: first.Format(time.DateOnly), to: month.AddDate(0, 1, -1).Format(time.DateOnly), accountID: accountID}
	where, args := filter.where()
	rows, err := a.store.DB.QueryContext(ctx, `SELECT substr(t.local_date, 1, 7),
        coalesce(sum(CASE WHEN t.kind = 'income' THEN t.eur_cents END), 0),
        coalesce(sum(CASE WHEN t.kind IN ('expense', 'refund') THEN -t.eur_cents END), 0)
    FROM transactions t`+registerJoins+where+` GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[string]trendMonth{}
	for rows.Next() {
		var total trendMonth
		if err := rows.Scan(&total.Month, &total.IncomeEURCents, &total.SpendingEURCents); err != nil {
			return nil, err
		}
		totals[total.Month] = total
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	months := make([]trendMonth, 0, dashboardMonths)
	for i := range dashboardMonths {
		key := first.AddDate(0, i, 0).Format("2006-01")
		total := totals[key]
		total.Month = key
		total.NetEURCents = total.IncomeEURCents - total.SpendingEURCents
		months = append(months, total)
	}
	return months, nil
}

// accountBalances returns each account's tracked balance at the end of the
// month and its change during the month, in the source currency and in EUR.
// Transfers count here because they move money in and out of accounts.
func (a *api) accountBalances(ctx context.Context, from, to string, accountID int64) ([]dashboardAccount, error) {
	query := `SELECT a.id, a.name, a.currency, a.archived,
        coalesce(sum(t.amount_minor), 0), coalesce(sum(t.eur_cents), 0),
        coalesce(sum(CASE WHEN t.local_date >= ? THEN t.amount_minor END), 0),
        coalesce(sum(CASE WHEN t.local_date >= ? THEN t.eur_cents END), 0),
        count(CASE WHEN t.local_date >= ? THEN 1 END)
    FROM accounts a
    LEFT JOIN transactions t ON t.account_id = a.id AND t.deleted_at IS NULL AND t.local_date <= ?`
	args := []any{from, from, from, to}
	if accountID != 0 {
		query, args = query+" WHERE a.id = ?", append(args, accountID)
	}
	rows, err := a.store.DB.QueryContext(ctx, query+" GROUP BY a.id ORDER BY a.archived, a.name COLLATE NOCASE, a.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []dashboardAccount{}
	for rows.Next() {
		var account dashboardAccount
		var archived, balance, change int64
		if err := rows.Scan(&account.ID, &account.Name, &account.Currency, &archived, &balance,
			&account.BalanceEURCents, &change, &account.ChangeEURCents, &account.TransactionCount); err != nil {
			return nil, err
		}
		account.Archived = archived == 1
		if account.Archived && accountID == 0 && balance == 0 && account.TransactionCount == 0 {
			continue
		}
		if account.Balance, err = decimal(balance, account.Currency); err != nil {
			return nil, err
		}
		if account.Change, err = decimal(change, account.Currency); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// pendingReview lists imports awaiting confirmation and counts transactions
// that still need a look. It covers every month, because unreviewed work
// does not belong to the month on screen.
func (a *api) pendingReview(ctx context.Context, accountID int64) (dashboardReview, error) {
	review := dashboardReview{Imports: []reviewBatch{}}
	batchFilter, batchArgs := "", []any{}
	if accountID != 0 {
		batchFilter, batchArgs = " AND b.source_account_id = ?", []any{accountID}
	}
	if err := a.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM import_batches b
        WHERE b.review_status = 'needs_review'`+batchFilter, batchArgs...).Scan(&review.ImportCount); err != nil {
		return review, err
	}
	rows, err := a.store.DB.QueryContext(ctx, `SELECT b.id, b.filename, a.id, a.name, a.currency, b.inserted_count,
        b.period_start, b.period_end, b.committed_at
    FROM import_batches b JOIN accounts a ON a.id = b.source_account_id
    WHERE b.review_status = 'needs_review'`+batchFilter+`
    ORDER BY b.committed_at, b.id LIMIT ?`, append(batchArgs, dashboardReviews)...)
	if err != nil {
		return review, err
	}
	defer rows.Close()
	for rows.Next() {
		var batch reviewBatch
		var start, end, committed sql.NullString
		if err := rows.Scan(&batch.ID, &batch.Filename, &batch.Account.ID, &batch.Account.Name, &batch.Account.Currency,
			&batch.InsertedCount, &start, &end, &committed); err != nil {
			return review, err
		}
		batch.PeriodStart, batch.PeriodEnd, batch.CommittedAt = nullText(start), nullText(end), nullText(committed)
		review.Imports = append(review.Imports, batch)
	}
	if err := rows.Err(); err != nil {
		return review, err
	}
	where, args := registerFilter{accountID: accountID}.where()
	err = a.store.DB.QueryRowContext(ctx, `SELECT count(CASE WHEN review = 'needs_review' THEN 1 END),
        count(CASE WHEN review = 'source_conflict' THEN 1 END)
    FROM (SELECT `+effectiveReview+` AS review FROM transactions t`+registerJoins+where+`
        AND t.review_status IN ('needs_review', 'source_conflict'))`, args...).Scan(&review.NeedsReviewCount, &review.ConflictCount)
	return review, err
}
