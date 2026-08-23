-- One baseline per experiment project. UpdateExpRunAnnotation clears siblings
-- then sets is_baseline inside a transaction, but two concurrent PATCHes that
-- both mark a previously-unset run can still both commit under READ COMMITTED:
-- the "clear others" updates lock no rows, and the two target rows never wait
-- on each other. The partial unique index is the actual invariant.

-- Concurrent races before this index may already have left more than one
-- baseline. Keep the most recently updated row so the index can be created.
UPDATE exp_runs SET is_baseline = FALSE
 WHERE is_baseline
   AND id NOT IN (
     SELECT DISTINCT ON (project_id) id
       FROM exp_runs
      WHERE is_baseline
      ORDER BY project_id, updated_at DESC, id DESC
   );

CREATE UNIQUE INDEX IF NOT EXISTS idx_exp_runs_one_baseline_per_project
    ON exp_runs (project_id)
    WHERE is_baseline;
