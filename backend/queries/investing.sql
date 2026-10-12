-- name: CreateInvestingSnapshot :one
INSERT INTO investing_snapshots (fetched_at, account_id, currency, total_value_minor, investments_value_minor,
    cost_basis_minor, unrealized_minor, realized_minor, cash_available_minor, cash_in_pies_minor, cash_reserved_minor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id;

-- Imported history takes the IDs below the snapshots already saved, because
-- snapshot IDs must grow with fetched_at.
-- name: ImportInvestingSnapshot :exec
INSERT INTO investing_snapshots (id, fetched_at, account_id, currency, total_value_minor, investments_value_minor,
    cost_basis_minor, unrealized_minor, realized_minor, cash_available_minor, cash_in_pies_minor, cash_reserved_minor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- Foreign keys must be deferred while the pie snapshots follow their IDs.
-- name: ShiftInvestingPieSnapshots :exec
UPDATE investing_pie_snapshots SET snapshot_id = snapshot_id + sqlc.arg(shift);

-- name: ShiftInvestingSnapshots :exec
UPDATE investing_snapshots SET id = id + sqlc.arg(shift);

-- name: MaxInvestingSnapshotID :one
SELECT CAST(coalesce(max(id), 0) AS INTEGER) FROM investing_snapshots;

-- name: EnsureInvestingPie :exec
INSERT INTO investing_pies (id) VALUES (?) ON CONFLICT DO NOTHING;

-- name: CreateInvestingPieSnapshot :exec
INSERT INTO investing_pie_snapshots (snapshot_id, pie_id, invested_minor, value_minor, result_minor, cash_minor,
    dividends_gained_minor, dividends_reinvested_minor, dividends_in_cash_minor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: LatestInvestingSnapshotAt :one
SELECT CAST(coalesce(max(fetched_at), '') AS TEXT) FROM investing_snapshots;

-- name: EarliestInvestingSnapshotAt :one
SELECT CAST(coalesce(min(fetched_at), '') AS TEXT) FROM investing_snapshots;

-- Pies seen in the latest snapshot whose name is missing or was checked
-- before the cutoff.
-- name: StaleInvestingPies :many
SELECT p.id FROM investing_pies p
JOIN investing_pie_snapshots s ON s.pie_id = p.id
WHERE s.snapshot_id = (SELECT max(id) FROM investing_snapshots)
    AND (p.name_checked_at IS NULL OR p.name_checked_at < sqlc.arg(cutoff))
ORDER BY p.name_checked_at IS NOT NULL, p.name_checked_at, p.id;

-- name: SetInvestingPieName :exec
UPDATE investing_pies SET name = coalesce(sqlc.narg(name), name), name_checked_at = sqlc.arg(checked_at) WHERE id = sqlc.arg(id);

-- name: NameInvestingPieIfMissing :exec
UPDATE investing_pies SET name = sqlc.arg(name), name_checked_at = sqlc.arg(checked_at) WHERE id = sqlc.arg(id) AND name IS NULL;

-- name: ListInvestingPies :many
SELECT * FROM investing_pies ORDER BY id;

-- The latest snapshot of each bucket of bucket_seconds since the cutoff, so
-- long ranges stay small. Snapshot IDs grow with fetched_at.
-- name: ListInvestingSnapshots :many
SELECT * FROM investing_snapshots WHERE id IN (
    SELECT max(b.id) FROM (
        SELECT a.id, CAST(strftime('%s', a.fetched_at) AS INTEGER) / CAST(sqlc.arg(bucket_seconds) AS INTEGER) AS bucket
        FROM investing_snapshots a WHERE a.fetched_at >= sqlc.arg(since)
    ) b GROUP BY b.bucket
) ORDER BY id;

-- name: ListInvestingPieSnapshots :many
SELECT * FROM investing_pie_snapshots WHERE snapshot_id IN (
    SELECT max(b.id) FROM (
        SELECT a.id, CAST(strftime('%s', a.fetched_at) AS INTEGER) / CAST(sqlc.arg(bucket_seconds) AS INTEGER) AS bucket
        FROM investing_snapshots a WHERE a.fetched_at >= sqlc.arg(since)
    ) b GROUP BY b.bucket
) ORDER BY snapshot_id, pie_id;

-- name: GetInvestingState :one
SELECT * FROM investing_state WHERE id = 1;

-- name: SetInvestingStart :exec
UPDATE investing_state SET started_on = ? WHERE id = 1;

-- name: RecordInvestingAttempt :exec
UPDATE investing_state SET last_attempt_at = ?, last_error = ? WHERE id = 1;

-- name: ListInvestingGroups :many
SELECT * FROM investing_groups ORDER BY normalized_name;

-- name: ListInvestingGroupPies :many
SELECT * FROM investing_group_pies ORDER BY group_id, pie_id;

-- name: CreateInvestingGroup :one
INSERT INTO investing_groups (name, normalized_name) VALUES (?, ?) RETURNING id;

-- name: RenameInvestingGroup :execrows
UPDATE investing_groups SET name = ?, normalized_name = ? WHERE id = ?;

-- name: DeleteInvestingGroup :execrows
DELETE FROM investing_groups WHERE id = ?;

-- name: ClearInvestingGroupPies :exec
DELETE FROM investing_group_pies WHERE group_id = ?;

-- name: AddInvestingGroupPie :exec
INSERT INTO investing_group_pies (group_id, pie_id) VALUES (?, ?);
