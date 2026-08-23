package store

import (
	"context"
	"log/slog"
	"time"
)

// RecordDownload increments today's counter for a repository in
// repo_download_stats, which backs the "downloads in the last 30 days"
// figure. Callers invoke this from a detached goroutine off the request path
// (see api.Server.recordDownload), so a failure here must never propagate --
// it is only worth logging, never worth failing a download over.
//
// "Today" is the UTC calendar day computed here rather than the database
// session's CURRENT_DATE, so the bucket a download lands in and the bucket
// DownloadsSince starts from agree regardless of the server's TimeZone.
func (s *Store) RecordDownload(ctx context.Context, repoID int64) {
	_, err := s.db.Exec(ctx, `
		INSERT INTO repo_download_stats (repo_id, date, count)
		VALUES ($1, $2, 1)
		ON CONFLICT (repo_id, date) DO UPDATE SET count = repo_download_stats.count + 1`,
		repoID, s.d.dateArg(utcDay(time.Now())))
	if err != nil {
		slog.Error("record download", "repo_id", repoID, "error", err)
	}
}

// DownloadsSince sums the daily counters from since (inclusive, by date) to
// today. The time of day is dropped so the comparison is against the same
// UTC calendar day RecordDownload writes, on both engines.
func (s *Store) DownloadsSince(ctx context.Context, repoID int64, since time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(count), 0) FROM repo_download_stats
		WHERE repo_id = $1 AND date >= $2`, repoID, s.d.dateArg(utcDay(since))).Scan(&n)
	return n, err
}

// utcDay is t's UTC calendar day at midnight: the key repo_download_stats
// rows are bucketed by.
func utcDay(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }
