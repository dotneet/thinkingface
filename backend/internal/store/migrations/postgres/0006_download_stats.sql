-- Daily download counters, used to answer "downloads in the last 30 days" on
-- a repository page without scanning history. Cumulative downloads keep
-- living on repositories.downloads (unchanged); this table only needs to
-- answer a bounded time-window query cheaply.
CREATE TABLE IF NOT EXISTS repo_download_stats (
    repo_id BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    date    DATE NOT NULL,
    count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_id, date)
);

CREATE INDEX IF NOT EXISTS idx_repo_download_stats_repo_date ON repo_download_stats (repo_id, date);
