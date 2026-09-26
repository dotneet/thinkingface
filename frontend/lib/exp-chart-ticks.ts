/**
 * Axis labelling for the uPlot charts of the experiment pages.
 *
 * uPlot's own formatter picks decimals from the tick increment but caps them,
 * so a learning-rate axis ticking 1e-4, 2e-4, 3e-4 printed "0 0 0 0". These
 * functions pick the precision from the ticks themselves: the fewest decimals
 * that print every tick exactly, a shared SI suffix for large magnitudes and
 * a shared exponent for tiny ones. Framework-free so it is asserted in tests
 * rather than eyeballed on a canvas.
 */

/** Most decimals a label is ever given. */
const MAX_DECIMALS = 8;

const SI = [
  { at: 1e12, suffix: "T" },
  { at: 1e9, suffix: "G" },
  { at: 1e6, suffix: "M" },
  { at: 1e3, suffix: "k" },
] as const;

/** From this magnitude on, labels use an SI suffix (15000 → "15k"). */
const LARGE = 1e4;
/** Below this magnitude, labels use a shared exponent (0.0003 → "3e-4"). */
const SMALL = 1e-3;

/**
 * The fewest decimals (0…MAX_DECIMALS) that print every value within a small
 * fraction of `step` of itself, so neighbouring ticks never collapse onto the
 * same label.
 */
function exactDecimals(values: readonly number[], step: number): number {
  const tolerance = Math.abs(step) * 1e-6 || 1e-12;
  for (let d = 0; d <= MAX_DECIMALS; d++) {
    const factor = 10 ** d;
    if (values.every((v) => Math.abs(Math.round(v * factor) / factor - v) <= tolerance)) {
      return d;
    }
  }
  return MAX_DECIMALS;
}

/** Smallest positive gap between two ticks, or the largest magnitude when there is none. */
function tickStep(values: readonly number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  let step = Number.POSITIVE_INFINITY;
  for (let i = 1; i < sorted.length; i++) {
    const gap = (sorted[i] ?? 0) - (sorted[i - 1] ?? 0);
    if (gap > 0 && gap < step) step = gap;
  }
  if (Number.isFinite(step)) return step;
  const max = Math.max(...values.map(Math.abs));
  return max > 0 ? max : 1;
}

const grouped = (decimals: number) =>
  new Intl.NumberFormat("en-US", {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });

/** One value on its own, for a logarithmic axis whose ticks share no step. */
export function formatTickValue(value: number): string {
  if (!Number.isFinite(value)) return "";
  if (value === 0) return "0";
  const magnitude = Math.abs(value);
  if (magnitude >= LARGE) {
    const unit = SI.find((u) => magnitude >= u.at) ?? SI[3];
    return `${Number((value / unit.at).toPrecision(3))}${unit.suffix}`;
  }
  if (magnitude < SMALL) {
    const [mantissa = "", exponent = ""] = value.toExponential(2).split("e");
    return `${Number(mantissa)}e${Number(exponent)}`;
  }
  return String(Number(value.toPrecision(4)));
}

/**
 * Labels for one axis's ticks. `log` formats each tick on its own (a log axis
 * ticks 1e-4, 1e-3, 1e-2 — there is no common step); a linear axis shares one
 * precision (and one suffix or exponent) across every tick so the labels line
 * up. A null or non-finite split gets an empty label.
 */
export function formatAxisTicks(splits: readonly (number | null)[], log = false): string[] {
  const finite = splits.filter((v): v is number => v !== null && Number.isFinite(v));
  if (finite.length === 0) return splits.map(() => "");
  const label = (v: number | null, format: (v: number) => string) =>
    v === null || !Number.isFinite(v) ? "" : format(v);

  if (log) return splits.map((v) => label(v, formatTickValue));

  const maxAbs = Math.max(...finite.map(Math.abs));
  const step = tickStep(finite);

  if (maxAbs >= LARGE) {
    const unit = SI.find((u) => maxAbs >= u.at) ?? SI[3];
    const scaled = finite.map((v) => v / unit.at);
    const decimals = exactDecimals(scaled, step / unit.at);
    return splits.map((v) =>
      label(v, (x) => (x === 0 ? "0" : `${(x / unit.at).toFixed(decimals)}${unit.suffix}`)),
    );
  }

  if (maxAbs > 0 && maxAbs < SMALL) {
    const exponent = Math.floor(Math.log10(maxAbs));
    const scale = 10 ** exponent;
    const decimals = exactDecimals(
      finite.map((v) => v / scale),
      step / scale,
    );
    return splits.map((v) =>
      label(v, (x) => (x === 0 ? "0" : `${(x / scale).toFixed(decimals)}e${exponent}`)),
    );
  }

  const format = grouped(exactDecimals(finite, step));
  return splits.map((v) => label(v, (x) => format.format(x)));
}

/**
 * Pixel width a y axis needs for its labels: uPlot's default is a fixed 50px,
 * which clips "12,345.5" and wastes room on "0.1". About 7px per character of
 * the widest label at the chart's 12px font, plus the tick and the gap.
 */
export function axisSizeFor(labels: readonly string[] | null | undefined): number {
  const longest = Math.max(1, ...(labels ?? []).map((label) => label.length));
  return Math.max(36, Math.ceil(longest * 7 + 18));
}

/**
 * A linear scale range around [min, max] with `frac` of the span as padding
 * on each side, instead of uPlot's default that anchors a y axis near zero.
 * The run scatter plot uses it: learning rates of 1e-4…1e-3 or CERs of
 * 0.04…0.09 drawn from zero are a flat line along the top.
 */
export function paddedRange(
  min: number | null,
  max: number | null,
  frac = 0.08,
): [number | null, number | null] {
  if (min === null || max === null || !Number.isFinite(min) || !Number.isFinite(max)) {
    return [min, max];
  }
  if (min === max) {
    const pad = Math.abs(min) * 0.1 || 1;
    return [min - pad, max + pad];
  }
  const pad = (max - min) * frac;
  return [min - pad, max + pad];
}

/**
 * A metric value for a narrow column of values read top to bottom (the run
 * sidebar): four significant digits *with* trailing zeros, so 17.60 and 14.13
 * line up digit for digit, and the tick formatter's suffix or exponent
 * outside the everyday range.
 */
export function formatColumnValue(value: number): string {
  if (!Number.isFinite(value)) return "";
  const magnitude = Math.abs(value);
  if (value === 0 || magnitude >= LARGE || magnitude < SMALL) return formatTickValue(value);
  return value.toPrecision(4);
}
