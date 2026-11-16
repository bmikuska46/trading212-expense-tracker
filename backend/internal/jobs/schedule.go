package jobs

import (
	"time"

	"budget-tracking/backend/internal/rates"
)

// Trading 212 jobs run at fixed Europe/Bratislava hours, an hour apart, so
// their calls to the shared report limits do not collide.
const (
	// DailyHour is when the open month is synced through the day before.
	// On the 1st that finishes the previous month.
	DailyHour = 1
	// SnapshotHour is when the portfolio is snapshotted each day.
	SnapshotHour = 2
	// IncomeHour is when dividends, interest, and cashback are read, on the
	// 1st of each month.
	IncomeHour = 3
)

// dailyAt is the first time at hour on a local day that is after instant.
func dailyAt(instant time.Time, hour int) time.Time {
	local := instant.In(rates.Location)
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, rates.Location)
	if at.After(instant) {
		return at
	}
	return time.Date(local.Year(), local.Month(), local.Day()+1, hour, 0, 0, 0, rates.Location)
}

// monthlyAt is the first time at hour on the 1st of a local month that is
// after instant.
func monthlyAt(instant time.Time, hour int) time.Time {
	local := instant.In(rates.Location)
	at := time.Date(local.Year(), local.Month(), 1, hour, 0, 0, 0, rates.Location)
	if at.After(instant) {
		return at
	}
	return time.Date(local.Year(), local.Month()+1, 1, hour, 0, 0, 0, rates.Location)
}
