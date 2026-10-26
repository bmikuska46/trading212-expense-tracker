-- report_from is where the range of a monthly job's current or last report
-- starts, before the overlap. A daily sync starts where the last one ended;
-- NULL means the month's start.
ALTER TABLE scheduled_jobs ADD COLUMN report_from TEXT;
