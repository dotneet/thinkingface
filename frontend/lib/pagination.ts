/**
 * Parses a `?offset=` query parameter into a non-negative integer, falling
 * back to 0 for anything else.
 *
 * Every list page reads this the same way it always has:
 * `Number(sp.offset ?? 0) || 0`. That only catches the non-numeric case
 * (`Number("abc")` is `NaN`, and `NaN || 0` is `0`) — a negative or
 * fractional value survives it untouched, because `-5 || 0` is `-5` and
 * `2.5 || 0` is `2.5`, both truthy. A hand-edited or bookmarked
 * `?offset=-5` (or `?offset=2.5`) then reaches `Pagination` and the list
 * APIs unchecked. This is the one place that check happens now, so a new
 * list page can't reintroduce the gap by copying the old expression.
 */
export function parseOffset(value: string | undefined | null): number {
  const n = Number(value ?? 0);
  return Number.isInteger(n) && n >= 0 ? n : 0;
}
