-- See the postgres migration (0035) for the reasoning.
ALTER TABLE exp_projects ADD COLUMN flush_blocked_at DATETIME;
ALTER TABLE exp_projects ADD COLUMN flush_error TEXT NOT NULL DEFAULT '';
