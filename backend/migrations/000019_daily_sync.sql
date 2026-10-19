-- report_to is where the range of a monthly job's current or last report
-- ends, before the overlap. A completed job whose report_to is before the
-- month's end is synced again the next day. Jobs from before daily syncs
-- always requested the whole month.
ALTER TABLE scheduled_jobs ADD COLUMN report_to TEXT;
UPDATE scheduled_jobs SET report_to = period_end_utc WHERE kind = 'monthly_import';
