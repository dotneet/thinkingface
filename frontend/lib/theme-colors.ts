/**
 * Theme colours for **canvas-drawn** charts (uPlot).
 *
 * Canvas2D assigns colours straight to `ctx.fillStyle` / `ctx.strokeStyle`,
 * and those setters run the CSS *colour* parser — not the custom-property
 * substitution the CSSOM performs for stylesheets. `ctx.fillStyle =
 * "var(--tf-fg-subtle)"` is therefore an invalid value that is *silently
 * ignored* (no exception), leaving whatever colour was there before — black on
 * a fresh context, i.e. black text on the dark theme's black canvas. Anything
 * handed to a canvas has to be resolved to a real colour first.
 *
 * SVG presentation attributes (`stroke="var(--tf-border)"`, as in
 * `parallel-coordinates.tsx`) *do* resolve `var()` and need none of this.
 */

export type ChartThemeColors = {
  /** Axis tick labels and the axis name — `--tf-fg-subtle`. */
  axis: string;
  /** Grid lines and ticks — `--tf-border`. */
  grid: string;
};

/**
 * Used whenever the real tokens cannot be read: SSR / the first render, or a
 * browser that hands back an empty string for the custom property. A mid grey
 * that stays legible on both the light and the dark canvas — the failure mode
 * this must never have is quietly falling back to black on a black background
 * (DESIGN.md §9: never conflate empty with a real value).
 */
export const CHART_THEME_FALLBACKS: ChartThemeColors = {
  axis: "#8b8f97",
  grid: "rgba(139, 143, 151, 0.35)",
};

const AXIS_TOKEN = "--tf-fg-subtle";
const GRID_TOKEN = "--tf-border";

/**
 * A resolved custom property comes back as a plain colour string, or as `""`
 * when the property is not set at all (`getPropertyValue` never throws). An
 * empty value is a failure, not a colour: fall back rather than let the canvas
 * keep its previous fill.
 */
export function normalizeCssColor(raw: string | null | undefined, fallback: string): string {
  const value = raw?.trim();
  return value ? value : fallback;
}

/** Reads the current theme's chart colours off `<html>`. Safe on the server. */
export function readChartThemeColors(): ChartThemeColors {
  if (typeof document === "undefined" || typeof getComputedStyle !== "function") {
    return { ...CHART_THEME_FALLBACKS };
  }
  const styles = getComputedStyle(document.documentElement);
  return {
    axis: normalizeCssColor(styles.getPropertyValue(AXIS_TOKEN), CHART_THEME_FALLBACKS.axis),
    grid: normalizeCssColor(styles.getPropertyValue(GRID_TOKEN), CHART_THEME_FALLBACKS.grid),
  };
}

/**
 * Calls `onChange` whenever the resolved theme changes, and returns the
 * unsubscribe. The app has exactly two ways for that to happen (see
 * `components/theme-toggle.tsx` and `app/globals.css`):
 *
 *  - `ThemeToggle` sets or removes `data-theme` on `<html>` — an attribute
 *    mutation, so a `MutationObserver` sees it;
 *  - with no `data-theme` (the "system" preference) the dark block is behind
 *    `prefers-color-scheme: dark`, so the OS switching themes changes the
 *    colours without touching the DOM at all — that needs `matchMedia`.
 *
 * Both fire only on an actual theme change, so a subscriber can repaint
 * without any risk of doing so on every render.
 */
export function subscribeThemeChange(onChange: () => void): () => void {
  if (typeof document === "undefined" || typeof window === "undefined") return () => {};

  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["data-theme"],
  });

  const media = window.matchMedia?.("(prefers-color-scheme: dark)");
  media?.addEventListener("change", onChange);

  return () => {
    observer.disconnect();
    media?.removeEventListener("change", onChange);
  };
}
