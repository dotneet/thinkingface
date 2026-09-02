"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ApiResult } from "@/lib/api";
import type { FailedApiResult } from "@/lib/api-error-message";

/** The shape every `{ items, total }` listing endpoint answers with. */
export type PagedListResponse = { items: unknown[]; total: number };

/**
 * How many consecutive renders with a reference-typed `deps` entry changing
 * identity count as "this is a bug, not a fast typist". React's own limit is
 * 50; this fires early enough to be the first thing in the console, and high
 * enough that StrictMode's double render cannot reach it on its own.
 */
const UNSTABLE_DEPS_RENDERS = 5;

/** Object, array or function — the values that are not stable by construction. */
function isReference(value: unknown): boolean {
  return (typeof value === "object" && value !== null) || typeof value === "function";
}

/** Element-wise `Object.is`, since `deps` is a fresh array on every render. */
function sameDeps(a: React.DependencyList, b: React.DependencyList): boolean {
  return a.length === b.length && a.every((value, i) => Object.is(value, b[i]));
}

/**
 * Everything `PaginationControls` needs, handed over as one object.
 *
 * The pager and this hook were deciding the same things independently and had
 * to agree for the screen to make sense: whether the window is past the end
 * (the hook's `outOfRange` chooses the empty state, the pager's chose whether
 * to draw itself, and one without the other gives you both or neither), and
 * how wide a page is (`pageSize` was passed twice, once to each). Passing the
 * state instead of the pieces removes the chance to disagree, and carries
 * `loading` along so every listing blocks its buttons while a page is out
 * rather than only the one screen that remembered to.
 */
export type PagerState = {
  offset: number;
  pageSize: number;
  /** `null` after a failed read — never 0 (DESIGN.md §9). */
  total: number | null;
  /** Rows that actually arrived for this page; `null` when the read failed. */
  loadedCount: number | null;
  /** Past the end of a list that is not empty — see `OutOfRangeEmptyState`. */
  outOfRange: boolean;
  /** A page is in flight: both buttons are blocked. */
  loading: boolean;
  onOffsetChange: (offset: number) => void;
};

export type UsePagedList<R extends PagedListResponse> = {
  /** The current page's rows, or `null` when the read failed or is still out. */
  items: R["items"] | null;
  /**
   * The whole successful response, for the fields beyond `items`/`total` a
   * particular endpoint carries (`default_quota_bytes`, …). `null` means "no
   * successful read", which is not the same as a field being absent.
   */
  data: R | null;
  /** `null` after a failed read — never fall back to 0 (DESIGN.md §9). */
  total: number | null;
  offset: number;
  setOffset: React.Dispatch<React.SetStateAction<number>>;
  loadError: string | null;
  /** True while a page requested by the effect is in flight. */
  loading: boolean;
  /** Re-read the current page. Mutation handlers call this after succeeding. */
  reload: () => Promise<void>;
  /** Past the end of a list that is not empty — see `OutOfRangeEmptyState`. */
  outOfRange: boolean;
  /** Pass straight to `PaginationControls`; see `PagerState`. */
  pager: PagerState;
};

/**
 * One offset-paged listing: the fetch, the page window, and the three states
 * a listing can be in.
 *
 * Five screens had hand-rolled this, comments included, and had already
 * drifted from each other. What lives here rather than at the call sites:
 *
 * - **The staleness ticket.** Every fetch, whoever started it, may only write
 *   state if it is still the newest one. The `cancelled` closure covers the
 *   effect's own supersession, but a mutation handler that reloads by calling
 *   `reload()` has no closure to be cancelled by, so a slow reload could
 *   otherwise land on top of a page the user has since moved to.
 * - **`total` going back to `null` on failure.** A count carried over from a
 *   successful read and printed next to an error state claims something the
 *   page does not know (DESIGN.md §9).
 * - **`outOfRange`.** Deleting the last row of the last page leaves the
 *   window past the end of a list that is not empty at all.
 *
 * `deps` is the caller's own input set — a search term, an organisation name,
 * a webhook id — and changing any of them re-reads *and* rewinds to the first
 * page: an offset only means anything against the result set it was chosen in,
 * so paging to offset 200 and then typing a search that matches three rows
 * used to request page 11 of the new list and land on the out-of-range empty
 * state. `deps` is compared element-wise with `Object.is`, so every entry has
 * to be a stable value — a primitive, or an object identity that survives a
 * re-render — or the rewind fires on every render.
 *
 * **The translator is deliberately not one of them.** Two of the five screens
 * used to fold `t` (or a `describe` built on it) into the dependencies, so
 * switching language refetched the whole listing; the labels re-render on
 * their own, and only `loadError` is a string frozen at fetch time. Keeping
 * `t` out is the behaviour the other three already had, and it is the cheaper
 * of the two.
 */
export function usePagedList<R extends PagedListResponse>({
  pageSize,
  deps,
  fetchPage,
  describe,
}: {
  pageSize: number;
  /**
   * Inputs other than `offset` that select what is listed — a search term, an
   * organisation name, a webhook id.
   *
   * **Every entry has to be a stable value**: a primitive, or an object
   * identity that survives a re-render. The array itself is rebuilt on every
   * render and is compared element-wise with `Object.is`, so a literal
   * (`deps: [{ org, kind }]`, `deps: [tags.filter(...)]`, `deps: [() => …]`)
   * never compares equal to the previous one — the rewind below then fires on
   * every render and React kills the component with "Maximum update depth
   * exceeded". Pass the primitives out of the object, or `useMemo` the value
   * before it gets here. A dev-only guard names the offending index if this
   * is got wrong.
   */
  deps: React.DependencyList;
  fetchPage: (params: { limit: number; offset: number }) => Promise<ApiResult<R>>;
  /** Turns a failed result into the message the error state shows. */
  describe: (result: FailedApiResult) => string;
}): UsePagedList<R> {
  const [data, setData] = useState<R | null>(null);
  const [total, setTotal] = useState<number | null>(null);
  const [offset, setOffset] = useState(0);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  // A change to `deps` selects a different list, so the window into it starts
  // over. Adjusted during render rather than in an effect: an effect would run
  // after this render's fetch had already been scheduled for the old offset,
  // so switching from page 2 would fire a request for page 2 of the new list
  // and immediately abandon it. React discards this render and re-runs the
  // component with the new state instead, so only one fetch is ever committed.
  const [shownDeps, setShownDeps] = useState<React.DependencyList>(deps);
  const depsChanged = !sameDeps(shownDeps, deps);
  if (depsChanged) {
    setShownDeps(deps);
    if (offset !== 0) setOffset(0);
  }

  // Dev-only guard for the single way `deps` can be got wrong (see its doc
  // comment): an entry that is a fresh object/array/function every render.
  // React's own answer to that is "Maximum update depth exceeded" with a stack
  // that points at React, so the call site never learns which entry it was.
  // Counted only for entries that are *reference* types on both sides — a
  // string changing five renders running is someone typing in a search box,
  // which is exactly what this hook is for.
  const unstableStreak = useRef(0);
  if (process.env.NODE_ENV !== "production") {
    const suspects = depsChanged
      ? deps.reduce<number[]>((acc, value, i) => {
          const previous = shownDeps[i];
          // The entry that *changed*, and changed between two reference
          // values: a stable object sitting next to a search term must not be
          // blamed for the search term's keystrokes.
          if (!Object.is(value, previous) && isReference(value) && isReference(previous)) {
            acc.push(i);
          }
          return acc;
        }, [])
      : [];
    unstableStreak.current = suspects.length > 0 ? unstableStreak.current + 1 : 0;
    if (unstableStreak.current === UNSTABLE_DEPS_RENDERS) {
      console.error(
        `usePagedList: deps entr${suspects.length > 1 ? "ies" : "y"} ` +
          `[${suspects.join(", ")}] changed identity on ${UNSTABLE_DEPS_RENDERS} renders in a ` +
          "row, so the list rewinds to page 1 on every render and will hit React's update-depth " +
          "limit. Pass a primitive, or memoise the value at the call site.",
      );
    }
  }

  // Both callbacks are re-created on every render by their call sites, and
  // `describe` closes over the translator. Reading them through a ref keeps
  // them out of `refresh`'s dependencies, so what re-reads is exactly `deps`.
  const fetchPageRef = useRef(fetchPage);
  const describeRef = useRef(describe);
  useEffect(() => {
    fetchPageRef.current = fetchPage;
    describeRef.current = describe;
  });

  const latestRequest = useRef(0);

  const refresh = useCallback(
    async (isStale: () => boolean = () => false) => {
      const ticket = ++latestRequest.current;
      const result = await fetchPageRef.current({ limit: pageSize, offset });
      if (isStale() || ticket !== latestRequest.current) return;
      if (!result.ok) {
        setLoadError(describeRef.current(result));
        setData(null);
        setTotal(null);
        return;
      }
      setLoadError(null);
      setData(result.data);
      setTotal(result.data.total);
    },
    // `deps` is the caller's declared input set (see the doc comment above);
    // a spread is the only way to fold it in, and it is what makes the rule
    // unable to verify this list statically.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pageSize, offset, ...deps],
  );

  // Guards against a fast search/page change letting an older, slower response
  // land after the newer one and overwrite it (typing "alice" then clearing
  // the box could otherwise show alice's single result after the full list had
  // already rendered). The in-flight flag lives here rather than inside
  // `refresh` so a superseded fetch cannot leave it stuck on: whichever effect
  // is current sets it, and only that same effect clears it.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    refresh(() => cancelled).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [refresh]);

  const reload = useCallback(() => refresh(), [refresh]);

  const items = (data?.items ?? null) as R["items"] | null;
  // The one place this is decided. The empty state and the pager are two
  // halves of the same answer, so they read the same boolean rather than each
  // recomputing it (see `PagerState`).
  const outOfRange = total !== null && total > 0 && offset >= total;

  return {
    items,
    data,
    total,
    offset,
    setOffset,
    loadError,
    loading,
    reload,
    outOfRange,
    pager: {
      offset,
      pageSize,
      total,
      // What actually arrived, not the window's width — see the range line in
      // `PaginationControls`.
      loadedCount: items?.length ?? null,
      outOfRange,
      loading,
      onOffsetChange: setOffset,
    },
  };
}
