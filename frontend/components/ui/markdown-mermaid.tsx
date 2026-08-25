"use client";

import { useEffect, useId, useState } from "react";
import { MarkdownCodeBlock } from "@/components/ui/markdown-code-block";
import { Skeleton } from "@/components/ui/skeleton";
import { subscribeThemeChange } from "@/lib/theme-colors";

/** Resolves the same light/dark decision `app/globals.css`'s cascade makes (DESIGN.md §1). */
function isDarkTheme(): boolean {
  if (typeof document === "undefined") return false;
  const attr = document.documentElement.getAttribute("data-theme");
  if (attr === "dark") return true;
  if (attr === "light") return false;
  return (
    typeof window !== "undefined" &&
    (window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false)
  );
}

type RenderState = { status: "loading" } | { status: "ok"; svg: string } | { status: "error" };

/**
 * Renders a ```mermaid fence as an SVG diagram, client-side only.
 *
 * README / model-card Markdown is untrusted author input (see the sanitising
 * policy in `lib/markdown-sanitize.ts`). This component never sees raw HTML
 * from that pipeline: `components/ui/markdown.tsx`'s `pre` handler hands it
 * the fence's plain text — extracted with `hastText` *before* `rehype-raw` /
 * `rehype-sanitize` even run on the wrapping `<pre>` — so nothing here rides
 * on the markdown sanitiser at all. What this component receives is diagram
 * *syntax*, not markup.
 *
 * mermaid's own `securityLevel: "strict"` (already the library default; set
 * explicitly so a future mermaid upgrade cannot silently loosen it) is what
 * keeps that syntax from turning into a script: it forces `htmlLabels` off
 * and runs every label through DOMPurify before the SVG is returned, so a
 * diagram cannot smuggle `<script>` / `on*` handlers into its own output.
 * `bindFunctions` (mermaid's opt-in wiring for `click` directives inside a
 * diagram) is deliberately never called, so even a diagram authored for
 * `securityLevel: "loose"` elsewhere cannot wire up a click handler here.
 * The returned SVG is trusted exactly as much as mermaid's own sanitisation —
 * this component does not sanitise it a second time.
 *
 * mermaid itself (~600KB) is dynamically imported, so pages whose Markdown
 * carries no ```mermaid fence never pay for it.
 *
 * A diagram that fails to parse — or the dynamic import failing to load —
 * falls back to the same fenced code block every other language gets
 * (`MarkdownCodeBlock`), so a broken diagram degrades to readable source
 * instead of taking the page down.
 */
export function MarkdownMermaid({ code }: { code: string }) {
  const rawId = useId();
  const diagramId = `tf-mermaid-${rawId.replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const [state, setState] = useState<RenderState>({ status: "loading" });
  // Bumped on every light/dark switch so the effect below re-renders the
  // diagram with mermaid's matching theme — a themed SVG bakes its colours
  // into `fill`/`stroke` attributes at render time, so a CSS-only follow-up
  // (the approach `uplot-chart.tsx` uses for its canvas) cannot repaint it.
  const [themeTick, setThemeTick] = useState(0);

  useEffect(() => subscribeThemeChange(() => setThemeTick((tick) => tick + 1)), []);

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });

    async function render() {
      try {
        const { default: mermaid } = await import("mermaid");
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: isDarkTheme() ? "dark" : "default",
          themeVariables: { fontFamily: "var(--font-sans)" },
        });
        const { svg } = await mermaid.render(diagramId, code);
        if (!cancelled) setState({ status: "ok", svg });
      } catch {
        if (!cancelled) setState({ status: "error" });
      }
    }

    void render();
    return () => {
      cancelled = true;
    };
    // `themeTick` is listed only to force a re-render on theme change; its
    // value itself carries no information the effect needs.
  }, [code, diagramId, themeTick]);

  if (state.status === "error") {
    return <MarkdownCodeBlock language="mermaid">{code}</MarkdownCodeBlock>;
  }
  if (state.status === "loading") {
    return <Skeleton className="tf-mermaid h-40 w-full" />;
  }
  return (
    <div
      className="tf-mermaid flex justify-center overflow-x-auto"
      // The SVG string comes straight from mermaid.render() above; see the
      // doc comment on this component for why that is safe to inject
      // directly instead of re-sanitising it here.
      // biome-ignore lint/security/noDangerouslySetInnerHtml: mermaid's own securityLevel:"strict" output, not author HTML — see doc comment above
      dangerouslySetInnerHTML={{ __html: state.svg }}
    />
  );
}
