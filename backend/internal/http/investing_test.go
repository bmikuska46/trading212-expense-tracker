package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"budget-tracking/backend/internal/database/dbgen"
)

func (app *testApp) snapshot(at time.Time, account string, value int64, pies map[int64]int64) {
	app.t.Helper()
	ctx := context.Background()
	q := app.store.Queries
	id, err := q.CreateInvestingSnapshot(ctx, dbgen.CreateInvestingSnapshotParams{FetchedAt: at.UTC().Format(time.RFC3339),
		AccountID: account, Currency: "EUR", TotalValueMinor: value, InvestmentsValueMinor: value, CostBasisMinor: 1000})
	if err != nil {
		app.t.Fatal(err)
	}
	for pie, pieValue := range pies {
		if err := q.EnsureInvestingPie(ctx, pie); err != nil {
			app.t.Fatal(err)
		}
		if err := q.CreateInvestingPieSnapshot(ctx, dbgen.CreateInvestingPieSnapshotParams{SnapshotID: id, PieID: pie,
			InvestedMinor: 100, ValueMinor: pieValue, ResultMinor: pieValue - 100}); err != nil {
			app.t.Fatal(err)
		}
	}
}

func TestInvestingSnapshots(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now }})
	cookie := app.setup()
	view := decode[investingView](t, app.expect(http.StatusOK, "GET", "/api/investing", nil, cookie))
	if view.Configured || view.Range != "30d" || view.Currency != nil || len(view.Snapshots) != 0 || len(view.Pies) != 0 {
		t.Fatalf("empty view %+v", view)
	}
	app.expect(http.StatusUnprocessableEntity, "GET", "/api/investing?range=2w", nil, cookie)

	// An older key for another account, then two days of hourly snapshots.
	app.snapshot(now.Add(-72*time.Hour), "1", 500, nil)
	for hour := 47; hour >= 0; hour-- {
		app.snapshot(now.Add(-time.Duration(hour)*time.Hour), "2", 2000-int64(hour), map[int64]int64{9: 150, 4: 120})
	}
	if err := app.store.Queries.SetInvestingPieName(context.Background(), dbgen.SetInvestingPieNameParams{
		Name: nullString("Growth"), CheckedAt: nullString("2026-09-30T12:00:00Z"), ID: 9}); err != nil {
		t.Fatal(err)
	}
	view = decode[investingView](t, app.expect(http.StatusOK, "GET", "/api/investing?range=7d", nil, cookie))
	if len(view.Snapshots) != 48 || *view.Currency != "EUR" {
		t.Fatalf("hourly range has %d snapshots", len(view.Snapshots))
	}
	last := view.Snapshots[47]
	if last.At != "2026-09-30T12:00:00Z" || last.TotalValueMinor != 2000 || len(last.Pies) != 2 ||
		last.Pies[0].ID != 4 || last.Pies[1].ResultMinor != 50 {
		t.Fatalf("latest snapshot %+v", last)
	}
	if len(view.Pies) != 2 || view.Pies[0].Name != nil || *view.Pies[1].Name != "Growth" {
		t.Fatalf("pies %+v", view.Pies)
	}
	// Longer ranges keep the latest snapshot of each bucket.
	view = decode[investingView](t, app.expect(http.StatusOK, "GET", "/api/investing?range=1y", nil, cookie))
	if len(view.Snapshots) != 3 || view.Snapshots[2].TotalValueMinor != 2000 || view.Snapshots[1].At != "2026-09-29T23:00:00Z" {
		t.Fatalf("daily range %+v", view.Snapshots)
	}

	app.expect(http.StatusUnprocessableEntity, "PUT", "/api/investing/settings", map[string]any{"startedOn": "2026-10-01"}, cookie)
	app.expect(http.StatusUnprocessableEntity, "PUT", "/api/investing/settings", map[string]any{"startedOn": "22.2.2024"}, cookie)
	app.expect(http.StatusOK, "PUT", "/api/investing/settings", map[string]any{"startedOn": "2024-02-22"}, cookie)
	view = decode[investingView](t, app.expect(http.StatusOK, "GET", "/api/investing", nil, cookie))
	if view.StartedOn == nil || *view.StartedOn != "2024-02-22" {
		t.Fatalf("start %v", view.StartedOn)
	}
	app.expect(http.StatusOK, "PUT", "/api/investing/settings", map[string]any{"startedOn": nil}, cookie)
	if view = decode[investingView](t, app.expect(http.StatusOK, "GET", "/api/investing", nil, cookie)); view.StartedOn != nil {
		t.Fatal("start was not cleared")
	}
}

func TestInvestingGroups(t *testing.T) {
	app := newTestApp(t, Options{})
	cookie := app.setup()
	app.snapshot(time.Now(), "2", 1000, map[int64]int64{1: 100, 2: 100, 3: 100})
	group := decode[investingGroupView](t, app.expect(http.StatusCreated, "POST", "/api/investing/groups",
		map[string]any{"name": "  Core ", "pieIds": []int64{2, 1, 2}}, cookie))
	if group.Name != "Core" || len(group.PieIDs) != 2 || group.PieIDs[0] != 1 {
		t.Fatalf("group %+v", group)
	}
	app.expect(http.StatusUnprocessableEntity, "POST", "/api/investing/groups", map[string]any{"name": "CORE", "pieIds": []int64{}}, cookie)
	app.expect(http.StatusUnprocessableEntity, "POST", "/api/investing/groups", map[string]any{"name": "Other", "pieIds": []int64{7}}, cookie)
	app.expect(http.StatusUnprocessableEntity, "POST", "/api/investing/groups", map[string]any{"name": " ", "pieIds": []int64{}}, cookie)
	path := "/api/investing/groups/" + strconv.FormatInt(group.ID, 10)
	// Renaming a group to its own name in another case is allowed.
	group = decode[investingGroupView](t, app.expect(http.StatusOK, "PUT", path, map[string]any{"name": "CORE", "pieIds": []int64{3}}, cookie))
	if group.Name != "CORE" || len(group.PieIDs) != 1 || group.PieIDs[0] != 3 {
		t.Fatalf("updated %+v", group)
	}
	app.expect(http.StatusNotFound, "PUT", "/api/investing/groups/99", map[string]any{"name": "X", "pieIds": []int64{}}, cookie)
	view := decode[investingView](t, app.expect(http.StatusOK, "GET", "/api/investing", nil, cookie))
	if len(view.Groups) != 1 || view.Groups[0].ID != group.ID {
		t.Fatalf("groups %+v", view.Groups)
	}
	app.expect(http.StatusNoContent, "DELETE", path, nil, cookie)
	app.expect(http.StatusNotFound, "DELETE", path, nil, cookie)
}
