/**
 * react-query cache keys for the experiment pages.
 *
 * Framework-free and shared rather than spelled out at each `useQuery` call:
 * the dashboard (many selected runs) and the single-run page (one run) read
 * the *same* metrics endpoint, so their keys have to agree exactly or the two
 * pages quietly stop reusing each other's cache — and, worse, have to get the
 * escaping below right independently.
 */

/**
 * Identifies one metrics read: a set of runs plus the x axis they are plotted
 * against.
 *
 * The run list is serialized with `JSON.stringify`, never `join(",")`. Run
 * names may contain commas — `lib/experiments.ts` builds sweep names like
 * `lr=0.1,bs=32`, which is the reason the metrics API takes a repeated `runs=`
 * parameter instead of one comma-joined value — so a comma-join collapses
 * `["lr=0.1,bs=32"]` and `["lr=0.1", "bs=32"]` onto the same key and serves
 * one query's series to the other. Same class of bug as the Parquet viewer's
 * column list (components/parquet/parquet-viewer.tsx).
 *
 * Order matters, as it did with the join: two selections of the same runs in a
 * different order are different keys. That costs one extra fetch in a rare
 * case and never mixes two results up, which is the trade the previous key
 * made too.
 */
export function metricsQueryKey(
  ns: string,
  repo: string,
  project: string,
  runs: readonly string[],
  xMode: string,
): string[] {
  return ["exp-metrics", ns, repo, project, JSON.stringify(runs), xMode];
}
