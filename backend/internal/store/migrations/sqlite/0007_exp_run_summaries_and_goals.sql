-- See the postgres migration (0007) for the reasoning.
ALTER TABLE exp_runs ADD COLUMN summary_min TEXT NOT NULL DEFAULT '{}';
ALTER TABLE exp_runs ADD COLUMN summary_max TEXT NOT NULL DEFAULT '{}';
ALTER TABLE exp_runs ADD COLUMN heartbeat_secs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE exp_projects ADD COLUMN metric_goals TEXT NOT NULL DEFAULT '{}';
