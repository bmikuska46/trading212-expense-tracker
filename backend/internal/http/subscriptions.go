package httpserver

import (
	"cmp"
	"context"
	"database/sql"
	"net/http"
	"slices"
	"time"
)

// subscriptionTolerance is how far a payment may land from its monthly due
// date, because banks move charges that fall on a weekend or holiday.
const subscriptionTolerance = 4 * 24 * time.Hour

// subscriptionPayment is one expense considered for a subscription. Amounts
// are positive magnitudes.
type subscriptionPayment struct {
	ID           int64
	PayeeID      int64
	PayeeName    string
	Account      accountRef
	CategoryID   *int64
	CategoryName string
	Date         time.Time
	Minor        int64
	Currency     string
	EURCents     int64
}

// subscriptionRun is a series of payments of the same payee, currency and
// amount, each one month after the previous within the tolerance.
type subscriptionRun struct {
	payments []subscriptionPayment
	// previous is the run this one continues after a price change.
	previous *subscriptionRun
}

func (run *subscriptionRun) first() subscriptionPayment { return run.payments[0] }
func (run *subscriptionRun) last() subscriptionPayment  { return run.payments[len(run.payments)-1] }

// due is the date the run's next payment is expected. Due dates count whole
// months from the first payment, so a charge moved by a weekend does not
// shift the ones after it.
func (run *subscriptionRun) due() time.Time {
	return addMonths(run.first().Date, len(run.payments))
}

type subscriptionView struct {
	PayeeID        int64      `json:"payeeId"`
	PayeeName      string     `json:"payeeName"`
	CategoryID     *int64     `json:"categoryId"`
	CategoryName   string     `json:"categoryName"`
	Account        accountRef `json:"account"`
	Amount         string     `json:"amount"`
	Currency       string     `json:"currency"`
	EURCents       int64      `json:"eurCents"`
	PreviousAmount *string    `json:"previousAmount"`
	Months         int        `json:"months"`
	PaidEURCents   int64      `json:"paidEurCents"`
	Since          string     `json:"since"`
	LastPaid       string     `json:"lastPaid"`
	NextDue        string     `json:"nextDue"`
	Active         bool       `json:"active"`
}

type subscriptionsView struct {
	Today           string             `json:"today"`
	MonthlyEURCents int64              `json:"monthlyEurCents"`
	YearlyEURCents  int64              `json:"yearlyEurCents"`
	Subscriptions   []subscriptionView `json:"subscriptions"`
}

func (a *api) listSubscriptions(w http.ResponseWriter, r *http.Request, current *session) {
	now := a.now().In(a.location(current))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	payments, err := a.subscriptionPayments(r.Context())
	if err != nil {
		internalError(w, "load subscription payments")
		return
	}
	view, err := buildSubscriptions(findSubscriptions(payments), today)
	if err != nil {
		internalError(w, "build subscriptions")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// subscriptionPayments loads every expense with a payee, ordered so the
// payments of one payee, currency and amount are adjacent and by date.
func (a *api) subscriptionPayments(ctx context.Context) ([]subscriptionPayment, error) {
	rows, err := a.store.DB.QueryContext(ctx, `SELECT t.id, t.payee_id, p.name, a.id, a.name, a.currency,
        t.category_id, coalesce(c.name, ''), t.local_date, -t.amount_minor, t.currency, -t.eur_cents
    FROM transactions t
    JOIN accounts a ON a.id = t.account_id
    JOIN payees p ON p.id = t.payee_id
    LEFT JOIN categories c ON c.id = t.category_id
    WHERE t.deleted_at IS NULL AND t.kind = 'expense'
    ORDER BY t.payee_id, t.currency, t.amount_minor, t.local_date, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	payments := []subscriptionPayment{}
	for rows.Next() {
		var payment subscriptionPayment
		var category sql.NullInt64
		var date string
		if err := rows.Scan(&payment.ID, &payment.PayeeID, &payment.PayeeName, &payment.Account.ID, &payment.Account.Name,
			&payment.Account.Currency, &category, &payment.CategoryName, &date, &payment.Minor, &payment.Currency,
			&payment.EURCents); err != nil {
			return nil, err
		}
		if category.Valid {
			payment.CategoryID = &category.Int64
		}
		if payment.Date, err = time.Parse(time.DateOnly, date); err != nil {
			return nil, err
		}
		payments = append(payments, payment)
	}
	return payments, rows.Err()
}

// findSubscriptions finds the latest run of at least two monthly payments
// for each payee, currency and amount. A run that starts when another run of
// the same payee and currency was due is the same subscription after a
// price change, so it replaces that run. Payments must be ordered by payee,
// currency, amount and date.
func findSubscriptions(payments []subscriptionPayment) []*subscriptionRun {
	runs := []*subscriptionRun{}
	for start := 0; start < len(payments); {
		end := start + 1
		for end < len(payments) && sameCharge(payments[start], payments[end]) {
			end++
		}
		if run := latestRun(payments[start:end]); run != nil {
			runs = append(runs, run)
		}
		start = end
	}
	slices.SortStableFunc(runs, func(x, y *subscriptionRun) int {
		return cmp.Or(cmp.Compare(x.first().PayeeID, y.first().PayeeID), cmp.Compare(x.first().Currency, y.first().Currency),
			x.first().Date.Compare(y.first().Date))
	})
	kept := []*subscriptionRun{}
	for _, run := range runs {
		for index, earlier := range kept {
			if earlier.first().PayeeID == run.first().PayeeID && earlier.first().Currency == run.first().Currency &&
				withinTolerance(run.first().Date, earlier.due()) {
				run.previous, kept = earlier, slices.Delete(kept, index, index+1)
				break
			}
		}
		kept = append(kept, run)
	}
	return kept
}

func sameCharge(x, y subscriptionPayment) bool {
	return x.PayeeID == y.PayeeID && x.Currency == y.Currency && x.Minor == y.Minor
}

// latestRun returns the last run of two or more payments among payments of
// one charge in date order, or nil when the charge never repeated monthly.
func latestRun(payments []subscriptionPayment) *subscriptionRun {
	var latest *subscriptionRun
	run := &subscriptionRun{payments: payments[:1]}
	for _, payment := range payments[1:] {
		if withinTolerance(payment.Date, run.due()) {
			run.payments = append(run.payments, payment)
			continue
		}
		if len(run.payments) >= 2 {
			latest = run
		}
		run = &subscriptionRun{payments: []subscriptionPayment{payment}}
	}
	if len(run.payments) >= 2 {
		latest = run
	}
	if latest != nil {
		// The runs share the backing array of payments; appending must not
		// overwrite the payments after a run.
		latest.payments = slices.Clip(latest.payments)
	}
	return latest
}

func withinTolerance(date, due time.Time) bool {
	difference := date.Sub(due)
	return difference >= -subscriptionTolerance && difference <= subscriptionTolerance
}

// addMonths moves a date by whole months and keeps the day, or the last day
// of a shorter month, so a charge on the 31st is due on the 28th of February.
func addMonths(date time.Time, months int) time.Time {
	first := time.Date(date.Year(), date.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(date.Day(), last)-1)
}

// buildSubscriptions lists active subscriptions by monthly cost, then ended
// ones by their last payment. A subscription stays active until its next
// payment is more than the tolerance overdue.
func buildSubscriptions(runs []*subscriptionRun, today time.Time) (subscriptionsView, error) {
	view := subscriptionsView{Today: today.Format(time.DateOnly), Subscriptions: []subscriptionView{}}
	for _, run := range runs {
		last := run.last()
		amount, err := decimal(last.Minor, last.Currency)
		if err != nil {
			return view, err
		}
		item := subscriptionView{PayeeID: last.PayeeID, PayeeName: last.PayeeName, CategoryID: last.CategoryID,
			CategoryName: last.CategoryName, Account: last.Account, Amount: amount, Currency: last.Currency,
			EURCents: last.EURCents, LastPaid: last.Date.Format(time.DateOnly), NextDue: run.due().Format(time.DateOnly),
			Active: !today.After(run.due().Add(subscriptionTolerance))}
		if item.CategoryID == nil {
			item.CategoryName = "Uncategorized"
		}
		if run.previous != nil {
			previous, err := decimal(run.previous.first().Minor, run.previous.first().Currency)
			if err != nil {
				return view, err
			}
			item.PreviousAmount = &previous
		}
		since := run
		for ; since != nil; since = since.previous {
			item.Months += len(since.payments)
			for _, payment := range since.payments {
				item.PaidEURCents += payment.EURCents
			}
			item.Since = since.first().Date.Format(time.DateOnly)
		}
		if item.Active {
			view.MonthlyEURCents += item.EURCents
		}
		view.Subscriptions = append(view.Subscriptions, item)
	}
	view.YearlyEURCents = 12 * view.MonthlyEURCents
	slices.SortStableFunc(view.Subscriptions, func(x, y subscriptionView) int {
		if x.Active != y.Active {
			if x.Active {
				return -1
			}
			return 1
		}
		if x.Active {
			return cmp.Or(cmp.Compare(y.EURCents, x.EURCents), cmp.Compare(x.PayeeName, y.PayeeName))
		}
		return cmp.Or(cmp.Compare(y.LastPaid, x.LastPaid), cmp.Compare(x.PayeeName, y.PayeeName))
	})
	return view, nil
}
