-- retry_count counts consecutive transient failures of a monthly import and
-- sets its backoff. Progress and manual reruns reset it; attempt_count keeps
-- numbering attempts across the job's whole history.
ALTER TABLE scheduled_jobs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0);
-- report_requested_at is when the job's current report was requested or
-- adopted, so a report that never finishes is given up on.
ALTER TABLE scheduled_jobs ADD COLUMN report_requested_at TEXT;
CREATE INDEX scheduled_jobs_batch ON scheduled_jobs(batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX job_attempts_stage ON job_attempts(stage, started_at);
CREATE INDEX notification_events_job ON notification_events(job_id) WHERE job_id IS NOT NULL;
