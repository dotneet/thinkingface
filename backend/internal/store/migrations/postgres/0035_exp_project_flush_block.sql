-- Why a project's metrics buffer cannot be flushed, and since when.
--
-- Two conditions are properties of the repository rather than of one attempt:
-- a project name no path can hold, and a metrics parquet with more rows than
-- a flush can rebuild in memory (experiments.maxExistingFlushRows). Retrying
-- either one every ten seconds is pointless, and worse than pointless: the
-- flush poller orders candidates by their oldest unflushed point, so a
-- permanently wedged project only climbs that order and eventually starves
-- every other project on the instance.
--
-- The previous answer was to delete the buffered points. That is silent data
-- loss -- the ingest API answered 200 for every one of them, and the API
-- documents no such limit -- so the points are now kept and the project is
-- marked instead. ListPendingFlushProjects skips a project blocked recently,
-- which both stops the starvation and lets the block expire on its own: once
-- an operator shrinks the file or renames the project, the next attempt after
-- the retry window succeeds and clears the mark. No manual unblock step.
ALTER TABLE exp_projects
    ADD COLUMN IF NOT EXISTS flush_blocked_at TIMESTAMPTZ;
ALTER TABLE exp_projects
    ADD COLUMN IF NOT EXISTS flush_error TEXT NOT NULL DEFAULT '';
