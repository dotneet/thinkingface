import type { Element } from "hast";
import { ExternalLink } from "lucide-react";
import Link from "next/link";
import ReactMarkdown, { type Components } from "react-markdown";
import { MarkdownCodeBlock } from "@/components/ui/markdown-code-block";
import { MarkdownHeadingAnchor } from "@/components/ui/markdown-heading-anchor";
import { MarkdownTable } from "@/components/ui/markdown-table";
import { cn } from "@/lib/cn";
import { markdownRehypePlugins, markdownRemarkPlugins } from "@/lib/markdown-pipeline";
import {
  isExternalHref,
  type MarkdownLinkContext,
  makeMarkdownUrlTransform,
} from "@/lib/markdown-urls";

// KaTeX ships its own stylesheet, and its output is meaningless without it
// (every glyph would collapse into a run of unpositioned spans). The App
// Router allows a global stylesheet import from a component, which keeps the
// dependency next to the plugin that needs it instead of in the root layout,
// where nothing would explain why it is there.
import "katex/dist/katex.min.css";

export type { MarkdownLinkContext };

export type MarkdownProps = {
  /** Raw Markdown source. */
  source: string;
  /** Base resolve URL of the document's directory, for relative assets (`./plot.png`). */
  assetBaseUrl?: string;
  /** Base resolve URL of the repository root, for root-relative assets (`/img/x.png`). */
  repoRootUrl?: string;
  /** Repository coordinates for turning relative links into in-app routes. */
  linkContext?: MarkdownLinkContext;
  /** Drop a leading YAML front matter block instead of rendering it as text. */
  stripFrontmatter?: boolean;
  /** Extra classes on the wrapping element (the wrapper always carries `tf-markdown`). */
  className?: string;
};

/** The `xxx` of a fence's `language-xxx` class, read off the child `<code>`. */
function fenceLanguage(node: Element | undefined): string | undefined {
  const code = node?.children.find((child) => child.type === "element" && child.tagName === "code");
  if (code?.type !== "element") return undefined;
  return classList(code)
    .find((c) => c.startsWith("language-"))
    ?.slice("language-".length);
}

function classList(node: Element | undefined): string[] {
  const value: unknown = node?.properties?.className;
  if (Array.isArray(value)) return value.map(String);
  if (typeof value === "string") return value.split(/\s+/).filter(Boolean);
  return [];
}

/**
 * Element → component mapping, shared by every `<Markdown>`. Hoisted to module
 * scope on purpose: react-markdown uses these functions as element *types*, so
 * a map rebuilt per render would make React treat every `<a>` / `<pre>` as a
 * new component and remount the whole subtree on each re-render (the editor
 * preview re-renders on every keystroke).
 */
const components: Components = {
  a({ node, href, children, ...props }) {
    // rehype-autolink-headings' permalink, tagged in markdown-pipeline.ts.
    if (classList(node).includes("tf-heading-anchor")) {
      const heading = node?.properties?.["data-heading"] ?? node?.properties?.dataHeading;
      return (
        <MarkdownHeadingAnchor href={href} heading={typeof heading === "string" ? heading : ""} />
      );
    }
    if (isExternalHref(href)) {
      return (
        // `rel` is not optional next to `target="_blank"`: without it the
        // opened page gets a handle on this one via `window.opener`.
        <a {...props} href={href} target="_blank" rel="noopener noreferrer">
          {children}
          <ExternalLink size={12} aria-hidden className="ml-0.5 inline-block align-baseline" />
        </a>
      );
    }
    // A link the URL transform turned into an in-app route: client-side
    // navigation, same as any other link in the app.
    if (href?.startsWith("/")) {
      return (
        <Link {...props} href={href}>
          {children}
        </Link>
      );
    }
    return (
      <a {...props} href={href}>
        {children}
      </a>
    );
  },
  img({ alt, src, title, width, height, className: imgClass }) {
    // Listed out rather than spread: react-markdown also hands over the hast
    // `node`, which React would try to set as a DOM attribute.
    return (
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={src}
        alt={alt ?? ""}
        title={title}
        width={width}
        height={height}
        className={imgClass}
        // A card can carry a dozen badge images; none of them should block
        // first paint.
        loading="lazy"
        decoding="async"
      />
    );
  },
  pre({ node, children }) {
    return <MarkdownCodeBlock language={fenceLanguage(node)}>{children}</MarkdownCodeBlock>;
  },
  table({ children }) {
    return <MarkdownTable>{children}</MarkdownTable>;
  },
};

/**
 * The single Markdown renderer for README cards, file previews, editor
 * previews and run notes. Usable from Server and Client Components alike.
 *
 * All remark/rehype plugin configuration, the element→component mapping and
 * URL resolution live here so every call site renders Markdown identically.
 *
 * This component is deliberately **not** `"use client"`: `ReadmeCard` is an
 * async Server Component, so the tree has to be renderable on the server. The
 * three pieces that need a translator (`MarkdownCodeBlock`,
 * `MarkdownHeadingAnchor`, `MarkdownTable`) are the client leaves, and nothing
 * else crosses the boundary.
 */
export function Markdown({
  source,
  assetBaseUrl,
  repoRootUrl,
  linkContext,
  stripFrontmatter = false,
  className,
}: MarkdownProps) {
  return (
    <div className={cn("tf-markdown", className)}>
      <ReactMarkdown
        remarkPlugins={markdownRemarkPlugins(stripFrontmatter)}
        rehypePlugins={markdownRehypePlugins()}
        urlTransform={makeMarkdownUrlTransform({ assetBaseUrl, repoRootUrl, linkContext })}
        components={components}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
}
