import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CHART_THEME_FALLBACKS,
  normalizeCssColor,
  readChartThemeColors,
  subscribeThemeChange,
} from "@/lib/theme-colors";

/** Stands in for `<html>`'s computed style with the given custom properties. */
function stubDocumentWithTokens(tokens: Record<string, string>) {
  vi.stubGlobal("document", { documentElement: {} });
  vi.stubGlobal("getComputedStyle", () => ({
    // The real CSSOM returns "" for a property that is not set.
    getPropertyValue: (name: string) => tokens[name] ?? "",
  }));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("normalizeCssColor", () => {
  it("returns the resolved value, trimmed", () => {
    // getPropertyValue keeps the leading space of `--tf-border: oklch(...)`.
    expect(normalizeCssColor(" oklch(0.88 0.008 265)", "#fff")).toBe("oklch(0.88 0.008 265)");
  });

  it("falls back when the property is unset, empty or whitespace", () => {
    expect(normalizeCssColor("", "#fff")).toBe("#fff");
    expect(normalizeCssColor("   ", "#fff")).toBe("#fff");
    expect(normalizeCssColor(null, "#fff")).toBe("#fff");
    expect(normalizeCssColor(undefined, "#fff")).toBe("#fff");
  });
});

describe("CHART_THEME_FALLBACKS", () => {
  it("is never black — the whole point is to stay visible on a dark canvas", () => {
    expect(CHART_THEME_FALLBACKS.axis).not.toMatch(/^#0{3,6}$/);
    expect(CHART_THEME_FALLBACKS.axis).not.toBe("black");
    expect(CHART_THEME_FALLBACKS.grid).not.toBe("black");
  });
});

describe("readChartThemeColors", () => {
  it("falls back without a document (server render)", () => {
    expect(readChartThemeColors()).toEqual(CHART_THEME_FALLBACKS);
  });

  it("resolves the tokens to real colour values", () => {
    stubDocumentWithTokens({
      "--tf-fg-subtle": " oklch(0.65 0.012 260)",
      "--tf-border": " oklch(0.32 0.014 260)",
    });
    expect(readChartThemeColors()).toEqual({
      axis: "oklch(0.65 0.012 260)",
      grid: "oklch(0.32 0.014 260)",
    });
  });

  it("falls back per token when only some resolve", () => {
    stubDocumentWithTokens({ "--tf-fg-subtle": "#123456" });
    expect(readChartThemeColors()).toEqual({
      axis: "#123456",
      grid: CHART_THEME_FALLBACKS.grid,
    });
  });

  it("never returns a var() reference, which a canvas cannot parse", () => {
    stubDocumentWithTokens({
      "--tf-fg-subtle": "#123456",
      "--tf-border": "#654321",
    });
    const colors = readChartThemeColors();
    expect(colors.axis).not.toContain("var(");
    expect(colors.grid).not.toContain("var(");
  });
});

describe("subscribeThemeChange", () => {
  it("is a no-op without a DOM", () => {
    expect(() => subscribeThemeChange(vi.fn())()).not.toThrow();
  });

  it("watches data-theme and prefers-color-scheme, and detaches both", () => {
    const observe = vi.fn();
    const disconnect = vi.fn();
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe = observe;
        disconnect = disconnect;
      },
    );
    const addEventListener = vi.fn();
    const removeEventListener = vi.fn();
    const matchMedia = vi.fn(() => ({ addEventListener, removeEventListener }));
    vi.stubGlobal("window", { matchMedia });
    vi.stubGlobal("document", { documentElement: {} });

    const onChange = vi.fn();
    const unsubscribe = subscribeThemeChange(onChange);

    expect(observe).toHaveBeenCalledWith({}, { attributes: true, attributeFilter: ["data-theme"] });
    expect(matchMedia).toHaveBeenCalledWith("(prefers-color-scheme: dark)");
    expect(addEventListener).toHaveBeenCalledWith("change", onChange);
    // Subscribing alone must not fire: a repaint only happens on a real change.
    expect(onChange).not.toHaveBeenCalled();

    unsubscribe();
    expect(disconnect).toHaveBeenCalledTimes(1);
    expect(removeEventListener).toHaveBeenCalledWith("change", onChange);
  });
});
