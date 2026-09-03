import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Contrast floor for the semantic colour tokens (DESIGN.md §1).
 *
 * The token values live in app/globals.css as oklch() triples, which say
 * nothing about legibility on their own — the light theme once shipped
 * `fg-subtle` at 4.28:1 and `text-warning` on `bg-warning/20` at 2.12:1 purely
 * because nobody converted them. This test does the conversion and asserts the
 * WCAG floor for every pair the design guide promises, in both themes, so a
 * future tweak to a single lightness value cannot quietly drop a pair below AA.
 *
 * Everything here is arithmetic on the stylesheet, so it belongs with the other
 * framework-free lib/ tests rather than in a browser harness.
 */

const CSS = readFileSync(fileURLToPath(new URL("../app/globals.css", import.meta.url)), "utf8");

type Rgb = readonly [number, number, number];

/** oklch(L C H) → sRGB, each channel 0–1 and gamut-clipped. */
function oklchToSrgb(lightness: number, chroma: number, hueDeg: number): Rgb {
  const hue = (hueDeg * Math.PI) / 180;
  const a = chroma * Math.cos(hue);
  const b = chroma * Math.sin(hue);
  const l = (lightness + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (lightness - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (lightness - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const linear = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  const encode = (x: number) => {
    const v = x <= 0.0031308 ? 12.92 * x : 1.055 * Math.max(x, 0) ** (1 / 2.4) - 0.055;
    return Math.min(1, Math.max(0, v));
  };
  return [encode(linear[0]!), encode(linear[1]!), encode(linear[2]!)] as const;
}

function relativeLuminance([r, g, b]: Rgb): number {
  const channel = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(fg: Rgb, bg: Rgb): number {
  const a = relativeLuminance(fg);
  const b = relativeLuminance(bg);
  const [hi, lo] = a > b ? [a, b] : [b, a];
  return (hi + 0.05) / (lo + 0.05);
}

/**
 * What the browser paints for `bg-warning/20` and friends: the token composited
 * over the surface underneath it at the given alpha.
 */
function tint(fg: Rgb, alpha: number, surface: Rgb): Rgb {
  return [
    fg[0] * alpha + surface[0] * (1 - alpha),
    fg[1] * alpha + surface[1] * (1 - alpha),
    fg[2] * alpha + surface[2] * (1 - alpha),
  ] as const;
}

/** Every `--tf-*: oklch(L C H)` declaration inside one brace-delimited block. */
function readTokens(blockStart: string): Map<string, Rgb> {
  const startIdx = CSS.indexOf(blockStart);
  if (startIdx === -1) throw new Error(`block not found in globals.css: ${blockStart}`);
  const open = CSS.indexOf("{", startIdx);
  // Brace matching rather than "next }": the dark values sit inside a
  // @media wrapper, so the first closing brace is not the end of the block.
  let depth = 0;
  let end = -1;
  for (let i = open; i < CSS.length; i++) {
    if (CSS[i] === "{") depth++;
    else if (CSS[i] === "}" && --depth === 0) {
      end = i;
      break;
    }
  }
  if (end === -1) throw new Error(`unbalanced braces after ${blockStart}`);

  const tokens = new Map<string, Rgb>();
  const re = /--tf-([a-z-]+):\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)/g;
  for (const [, name, l, c, h] of CSS.slice(open, end).matchAll(re)) {
    tokens.set(name!, oklchToSrgb(Number(l), Number(c), Number(h)));
  }
  if (tokens.size === 0) throw new Error(`no --tf-* tokens in ${blockStart}`);
  return tokens;
}

const THEMES = {
  light: readTokens(":root {"),
  dark: readTokens(':root[data-theme="dark"] {'),
};

const SURFACES = ["bg", "bg-raised", "bg-sunken", "bg-hover"] as const;
const FOREGROUNDS = ["fg", "fg-muted", "fg-subtle"] as const;

/** WCAG AA for text below 18.66px bold / 24px regular — i.e. all of our text. */
const AA_TEXT = 4.5;
/** WCAG 1.4.11 for control boundaries and focus indicators. */
const AA_NON_TEXT = 3;

describe.each(Object.keys(THEMES) as (keyof typeof THEMES)[])("%s theme", (theme) => {
  const t = THEMES[theme];
  const token = (name: string): Rgb => {
    const value = t.get(name);
    if (value === undefined) throw new Error(`--tf-${name} missing from the ${theme} block`);
    return value;
  };

  // fg-subtle is the most-used foreground token in the app and is held to the
  // same bar as fg — it is not a decorative grey (DESIGN.md §1).
  it.each(FOREGROUNDS.flatMap((fg) => SURFACES.map((bg) => [fg, bg] as const)))(
    "text-%s on bg-%s clears AA",
    (fg, bg) => {
      expect(contrast(token(fg), token(bg))).toBeGreaterThanOrEqual(AA_TEXT);
    },
  );

  it("text on the accent fill clears AA", () => {
    expect(contrast(token("accent-fg"), token("accent"))).toBeGreaterThanOrEqual(AA_TEXT);
  });

  // DESIGN.md §1: `text-warning` is documented as usable for "warning-coloured
  // text on a *neutral* surface" — i.e. the base status tokens, not just
  // `-strong`, are meant to double as body text. Nothing asserted that before:
  // in the light theme `warning` measured 2.42:1 on `bg` and `positive` 3.79:1
  // on `bg-hover`, both silently below AA. Covers every base status token
  // (not `-strong`, which is already covered by the tinted-fill cases below)
  // against every surface, in both themes, so a future lightness tweak can't
  // quietly drop one below the floor again the way it did here.
  it.each(["warning", "positive", "negative", "accent"] as const)(
    "text-%s clears AA as plain text on every surface",
    (statusToken) => {
      for (const surface of SURFACES) {
        expect(contrast(token(statusToken), token(surface)), surface).toBeGreaterThanOrEqual(
          AA_TEXT,
        );
      }
    },
  );

  // A tinted fill darkens the surface by the same hue as the label on it, so
  // these pairs are the ones that silently collapse. `-strong` exists for them.
  it.each([
    // label token, fill token, alpha (null = the fill is an opaque token)
    ["accent-strong", "accent-muted", null],
    ["positive-strong", "positive", 0.15],
    ["negative-strong", "negative", 0.15],
    ["warning-strong", "warning", 0.2],
    // Alert's tinted panels and the danger Button's hover state.
    ["positive-strong", "positive", 0.1],
    ["negative-strong", "negative", 0.1],
    ["warning-strong", "warning", 0.1],
  ] as const)("text-%s on the %s fill clears AA", (label, fill, alpha) => {
    for (const surface of SURFACES) {
      const painted =
        alpha === null ? token(fill) : tint(token(fill), alpha, token(surface as string));
      expect(contrast(token(label), painted)).toBeGreaterThanOrEqual(AA_TEXT);
      if (alpha === null) break; // an opaque fill does not depend on what is under it
    }
  });

  it("a control's boundary is distinguishable from both sides of it", () => {
    // The input's own fill on the inside, the card it sits on outside.
    expect(contrast(token("border-control"), token("bg-sunken"))).toBeGreaterThanOrEqual(
      AA_NON_TEXT,
    );
    expect(contrast(token("border-control"), token("bg-raised"))).toBeGreaterThanOrEqual(
      AA_NON_TEXT,
    );
  });

  it("the focus ring is visible on every surface", () => {
    for (const surface of SURFACES) {
      expect(contrast(token("accent"), token(surface))).toBeGreaterThanOrEqual(AA_NON_TEXT);
    }
  });
});

it("the two dark blocks declare identical values", () => {
  // The dark values are written twice — once under
  // `@media (prefers-color-scheme: dark)` for the system default and once under
  // `:root[data-theme="dark"]` so the toggle wins (DESIGN.md §1). Nothing but
  // this test stops the two copies from drifting.
  const media = readTokens(':root:not([data-theme="light"]) {');
  expect(Object.fromEntries(media)).toEqual(Object.fromEntries(THEMES.dark));
});
