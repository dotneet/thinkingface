-- Sweep grouping on a run (postgres/0025_exp_run_grouping.sql): the sweep it
-- belongs to and the role it played, as `init(group=..., job_type=...)`
-- declared them. Ingest columns, not annotations, but written with the same
-- "NULL means keep" rule, so a batch that omits them leaves them alone.
-- Empty string means "not in a group".
ALTER TABLE exp_runs ADD COLUMN group_name TEXT NOT NULL DEFAULT '';
ALTER TABLE exp_runs ADD COLUMN job_type TEXT NOT NULL DEFAULT '';
