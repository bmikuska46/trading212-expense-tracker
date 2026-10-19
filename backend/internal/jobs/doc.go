// Package jobs runs scheduled work inside the server process: the daily rate
// fetch, the daily Trading 212 transaction import, the notification email worker, the
// portfolio snapshots, and the investment income import.
// Each worker keeps its state in the database, so a restart resumes it
// instead of starting over.
package jobs
