package httpserver

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/jobs"
	"budget-tracking/backend/internal/ledger"
)

// investingRanges maps each range to how far back it reaches and how wide
// its buckets are. Each bucket keeps its latest snapshot, so a range stays
// at a few hundred points. All widens its buckets as history grows.
var investingRanges = map[string]struct{ span, bucket time.Duration }{
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"30d": {30 * 24 * time.Hour, 4 * time.Hour},
	"90d": {90 * 24 * time.Hour, 12 * time.Hour},
	"1y":  {365 * 24 * time.Hour, 24 * time.Hour},
	"all": {0, 24 * time.Hour},
}

const maxInvestingPoints = 500

type investingPieSnapshotView struct {
	ID                       int64 `json:"id"`
	InvestedMinor            int64 `json:"investedMinor"`
	ValueMinor               int64 `json:"valueMinor"`
	ResultMinor              int64 `json:"resultMinor"`
	CashMinor                int64 `json:"cashMinor"`
	DividendsGainedMinor     int64 `json:"dividendsGainedMinor"`
	DividendsReinvestedMinor int64 `json:"dividendsReinvestedMinor"`
	DividendsInCashMinor     int64 `json:"dividendsInCashMinor"`
}

type investingSnapshotView struct {
	At                    string                     `json:"at"`
	TotalValueMinor       int64                      `json:"totalValueMinor"`
	InvestmentsValueMinor int64                      `json:"investmentsValueMinor"`
	CostBasisMinor        int64                      `json:"costBasisMinor"`
	UnrealizedMinor       int64                      `json:"unrealizedMinor"`
	RealizedMinor         int64                      `json:"realizedMinor"`
	CashAvailableMinor    int64                      `json:"cashAvailableMinor"`
	CashInPiesMinor       int64                      `json:"cashInPiesMinor"`
	CashReservedMinor     int64                      `json:"cashReservedMinor"`
	Pies                  []investingPieSnapshotView `json:"pies"`
}

type investingPieView struct {
	ID   int64   `json:"id"`
	Name *string `json:"name"`
}

type investingGroupView struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	PieIDs []int64 `json:"pieIds"`
}

type investingView struct {
	Configured    bool                    `json:"configured"`
	Range         string                  `json:"range"`
	Currency      *string                 `json:"currency"`
	StartedOn     *string                 `json:"startedOn"`
	LastAttemptAt *string                 `json:"lastAttemptAt"`
	LastError     *string                 `json:"lastError"`
	Snapshots     []investingSnapshotView `json:"snapshots"`
	Pies          []investingPieView      `json:"pies"`
	Groups        []investingGroupView    `json:"groups"`
}

// getInvesting returns the snapshots of a range. Pies are listed whether or
// not they appear in the range, sorted by ID, so their colors stay stable.
func (a *api) getInvesting(w http.ResponseWriter, r *http.Request, _ *session) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "30d"
	}
	window, ok := investingRanges[name]
	if !ok {
		writeFieldErrors(w, map[string]string{"range": "Choose 7d, 30d, 90d, 1y, or all"})
		return
	}
	ctx, q := r.Context(), a.store.Queries
	_, _, configured, err := jobs.Trading212Credentials(ctx, q)
	if err != nil {
		internalError(w, "load integration")
		return
	}
	state, err := q.GetInvestingState(ctx)
	if err != nil {
		internalError(w, "load investing state")
		return
	}
	view := investingView{Configured: configured, Range: name, StartedOn: nullText(state.StartedOn),
		LastAttemptAt: nullText(state.LastAttemptAt), LastError: nullText(state.LastError),
		Snapshots: []investingSnapshotView{}}
	since, bucket := time.Time{}, window.bucket
	if window.span > 0 {
		since = a.now().Add(-window.span)
	} else {
		earliest, err := q.EarliestInvestingSnapshotAt(ctx)
		if err != nil {
			internalError(w, "load snapshots")
			return
		}
		if first, err := time.Parse(time.RFC3339, earliest); err == nil {
			bucket = max(bucket, a.now().Sub(first)/maxInvestingPoints)
		}
	}
	params := dbgen.ListInvestingSnapshotsParams{BucketSeconds: int64(bucket / time.Second), Since: since.UTC().Format(time.RFC3339)}
	snapshots, err := q.ListInvestingSnapshots(ctx, params)
	if err != nil {
		internalError(w, "load snapshots")
		return
	}
	pieRows, err := q.ListInvestingPieSnapshots(ctx, dbgen.ListInvestingPieSnapshotsParams(params))
	if err != nil {
		internalError(w, "load pie snapshots")
		return
	}
	byID := make(map[int64][]investingPieSnapshotView, len(snapshots))
	for _, row := range pieRows {
		byID[row.SnapshotID] = append(byID[row.SnapshotID], investingPieSnapshotView{ID: row.PieID,
			InvestedMinor: row.InvestedMinor, ValueMinor: row.ValueMinor, ResultMinor: row.ResultMinor, CashMinor: row.CashMinor,
			DividendsGainedMinor: row.DividendsGainedMinor, DividendsReinvestedMinor: row.DividendsReinvestedMinor,
			DividendsInCashMinor: row.DividendsInCashMinor})
	}
	// A key for another account starts a new history; the old one is kept
	// but not mixed into the charts.
	if len(snapshots) > 0 {
		latest := snapshots[len(snapshots)-1]
		view.Currency = &latest.Currency
		for _, snapshot := range snapshots {
			if snapshot.AccountID != latest.AccountID || snapshot.Currency != latest.Currency {
				continue
			}
			pies := byID[snapshot.ID]
			if pies == nil {
				pies = []investingPieSnapshotView{}
			}
			view.Snapshots = append(view.Snapshots, investingSnapshotView{At: snapshot.FetchedAt,
				TotalValueMinor: snapshot.TotalValueMinor, InvestmentsValueMinor: snapshot.InvestmentsValueMinor,
				CostBasisMinor: snapshot.CostBasisMinor, UnrealizedMinor: snapshot.UnrealizedMinor,
				RealizedMinor: snapshot.RealizedMinor, CashAvailableMinor: snapshot.CashAvailableMinor,
				CashInPiesMinor: snapshot.CashInPiesMinor, CashReservedMinor: snapshot.CashReservedMinor, Pies: pies})
		}
	}
	if view.Pies, err = a.investingPies(ctx); err != nil {
		internalError(w, "load pies")
		return
	}
	if view.Groups, err = a.investingGroups(ctx); err != nil {
		internalError(w, "load pie groups")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) investingPies(ctx context.Context) ([]investingPieView, error) {
	rows, err := a.store.Queries.ListInvestingPies(ctx)
	if err != nil {
		return nil, err
	}
	pies := make([]investingPieView, 0, len(rows))
	for _, row := range rows {
		pies = append(pies, investingPieView{ID: row.ID, Name: nullText(row.Name)})
	}
	return pies, nil
}

func (a *api) investingGroups(ctx context.Context) ([]investingGroupView, error) {
	q := a.store.Queries
	rows, err := q.ListInvestingGroups(ctx)
	if err != nil {
		return nil, err
	}
	members, err := q.ListInvestingGroupPies(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]investingGroupView, 0, len(rows))
	for _, row := range rows {
		group := investingGroupView{ID: row.ID, Name: row.Name, PieIDs: []int64{}}
		for _, member := range members {
			if member.GroupID == row.ID {
				group.PieIDs = append(group.PieIDs, member.PieID)
			}
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// updateInvestingSettings sets the date of the first deposit, which the
// average yearly return counts from. Null clears it.
func (a *api) updateInvestingSettings(w http.ResponseWriter, r *http.Request, _ *session) {
	var input struct {
		StartedOn *string `json:"startedOn"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var startedOn *string
	if input.StartedOn != nil && *input.StartedOn != "" {
		day, err := time.Parse(time.DateOnly, *input.StartedOn)
		if err != nil || day.Year() < 1900 || day.After(a.now()) {
			writeFieldErrors(w, map[string]string{"startedOn": "Use a past date as YYYY-MM-DD"})
			return
		}
		startedOn = input.StartedOn
	}
	value := nullString("")
	if startedOn != nil {
		value = nullString(*startedOn)
	}
	if err := a.store.Queries.SetInvestingStart(r.Context(), value); err != nil {
		internalError(w, "save investing start")
		return
	}
	writeJSON(w, http.StatusOK, map[string]*string{"startedOn": startedOn})
}

type groupInput struct {
	Name   string  `json:"name"`
	PieIDs []int64 `json:"pieIds"`
}

var errGroupFields = errors.New("invalid group")

// saveGroup validates a group and writes its name and pies. It reports
// field errors itself and returns errGroupFields after doing so.
func (a *api) saveGroup(w http.ResponseWriter, r *http.Request, id int64) (int64, error) {
	var input groupInput
	if !decodeJSON(w, r, &input) {
		return 0, errGroupFields
	}
	name := ledger.DisplayName(input.Name)
	if !ledger.ValidName(name) {
		writeFieldErrors(w, map[string]string{"name": "Enter a name of at most 100 characters"})
		return 0, errGroupFields
	}
	normalized := ledger.NormalizeName(name)
	err := a.inTx(r.Context(), func(q txn) error {
		groups, err := q.ListInvestingGroups(r.Context())
		if err != nil {
			return err
		}
		found := id == 0
		for _, group := range groups {
			if group.ID == id {
				found = true
			} else if group.NormalizedName == normalized {
				writeFieldErrors(w, map[string]string{"name": "Another group already has this name"})
				return errGroupFields
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "Group not found")
			return errGroupFields
		}
		pies, err := q.ListInvestingPies(r.Context())
		if err != nil {
			return err
		}
		for _, pie := range input.PieIDs {
			if !slices.ContainsFunc(pies, func(known dbgen.InvestingPie) bool { return known.ID == pie }) {
				writeFieldErrors(w, map[string]string{"pieIds": "Choose pies from the list"})
				return errGroupFields
			}
		}
		if id == 0 {
			if id, err = q.CreateInvestingGroup(r.Context(), dbgen.CreateInvestingGroupParams{Name: name, NormalizedName: normalized}); err != nil {
				return err
			}
		} else if _, err := q.RenameInvestingGroup(r.Context(), dbgen.RenameInvestingGroupParams{Name: name, NormalizedName: normalized, ID: id}); err != nil {
			return err
		}
		if err := q.ClearInvestingGroupPies(r.Context(), id); err != nil {
			return err
		}
		slices.Sort(input.PieIDs)
		for _, pie := range slices.Compact(input.PieIDs) {
			if err := q.AddInvestingGroupPie(r.Context(), dbgen.AddInvestingGroupPieParams{GroupID: id, PieID: pie}); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func (a *api) writeGroup(w http.ResponseWriter, r *http.Request, status int, id int64) {
	groups, err := a.investingGroups(r.Context())
	if err != nil {
		internalError(w, "load pie groups")
		return
	}
	for _, group := range groups {
		if group.ID == id {
			writeJSON(w, status, group)
			return
		}
	}
	writeError(w, http.StatusNotFound, "Group not found")
}

func (a *api) createInvestingGroup(w http.ResponseWriter, r *http.Request, _ *session) {
	id, err := a.saveGroup(w, r, 0)
	if errors.Is(err, errGroupFields) {
		return
	}
	if err != nil {
		internalError(w, "create pie group")
		return
	}
	a.writeGroup(w, r, http.StatusCreated, id)
}

func (a *api) updateInvestingGroup(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Group")
	if !ok {
		return
	}
	_, err := a.saveGroup(w, r, id)
	if errors.Is(err, errGroupFields) {
		return
	}
	if err != nil {
		internalError(w, "update pie group")
		return
	}
	a.writeGroup(w, r, http.StatusOK, id)
}

func (a *api) deleteInvestingGroup(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Group")
	if !ok {
		return
	}
	deleted, err := a.store.Queries.DeleteInvestingGroup(r.Context(), id)
	if err != nil {
		internalError(w, "delete pie group")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "Group not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
