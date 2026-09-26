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
 * `lr=0.1,bs=32`, which is the reason the metrics API takes a repeated `run=`
 * parameter instead of one comma-joined `runs=` value — so a comma-join collapses
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

/**
 * The `xMode` segment of a key built by `metricsQueryKey`, read back out of a
 * `Query`'s own `queryKey` (as seen from a `placeholderData` callback's
 * `previousQuery`).
 *
 * Both metrics queries (the dashboard's and the single-run page's) use
 * `placeholderData: keepPreviousData` so a run toggle or a live refetch
 * doesn't unmount the charts — but `keepPreviousData` keeps the previous
 * *key's* series regardless of what changed, including the x-mode itself.
 * Switching step/time changes `xIsTime` (and the CSV header) immediately
 * while the old mode's series stay on screen until the new response lands,
 * plotting step values on a time axis or vice versa. Comparing this against
 * the current `xMode` lets a query keep the placeholder only when the mode
 * actually matches.
 */
export function metricsQueryKeyXMode(queryKey: readonly unknown[]): string | undefined {
  const xMode = queryKey[5];
  return typeof xMode === "string" ? xMode : undefined;
}
