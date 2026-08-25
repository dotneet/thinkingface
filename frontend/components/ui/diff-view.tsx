import { cn } from "@/lib/cn";
import { type DiffLine, type DiffLineKind, parseUnifiedDiff } from "@/lib/diff";

/**
 * One file's unified diff, rendered as numbered rows.
 *
 * It lives here rather than next to the commit page because it is a look, not
 * a screen: the token set had nothing for "this line was added" and DESIGN.md
 * §1 says a new look becomes a primitive before it becomes a `className` in a
 * page. The added/removed tints are `bg-positive/10` and `bg-negative/10`,
 * which invert with the theme like every other surface, and the `+` / `-`
 * markers carry the same information without colour — the tint is
 * reinforcement, never the only signal.
 *
 * The block scrolls in **both** axes inside its own border: a long line must
 * not push the page sideways, and a long patch must not push the file list
 * off the bottom of the screen.
 */

/** Row fill. Context stays on the container's own surface. */
const ROW_TONES: Record<DiffLineKind, string> = {
  add: "bg-positive/10",
  del: "bg-negative/10",
  context: "",
  hunk: "bg-bg-hover",
  meta: "bg-bg-hover",
};

/**
 * The marker glyph sits on a fill of its own hue, so it takes the `-strong`
 * token (DESIGN.md §1) — it is on its own element, not the row's, so the
 * row's tint and this text never share a className.
 */
const MARK_TONES: Record<DiffLineKind, string> = {
  add: "text-positive-strong",
  del: "text-negative-strong",
  context: "text-fg-subtle",
  hunk: "text-fg-subtle",
  meta: "text-fg-subtle",
};

const MARKS: Record<DiffLineKind, string> = {
  add: "+",
  del: "-",
  context: "",
  hunk: "",
  meta: "",
};

const GUTTER = "w-14 shrink-0 select-none px-2 text-right font-medium text-fg-subtle tabular-nums";

function DiffRow({ line }: { line: DiffLine }) {
  if (line.kind === "hunk" || line.kind === "meta") {
    return (
      <div className={cn("flex", ROW_TONES[line.kind])}>
        <span className="whitespace-pre px-2 py-0.5 font-medium text-fg-subtle">{line.text}</span>
      </div>
    );
  }
  return (
    <div className={cn("flex", ROW_TONES[line.kind])}>
      <span className={GUTTER}>{line.oldLine ?? ""}</span>
      <span className={GUTTER}>{line.newLine ?? ""}</span>
      <span className={cn("w-4 shrink-0 select-none text-center", MARK_TONES[line.kind])}>
        {MARKS[line.kind]}
      </span>
      <span className="whitespace-pre pr-4 text-fg">{line.text}</span>
    </div>
  );
}

export function DiffView({
  patch,
  emptyLabel,
  truncatedNote,
  className,
}: {
  /** A unified diff body: hunks only, no `diff --git` / `---` / `+++` headers. */
  patch: string;
  /**
   * Shown when the patch holds no lines at all. No default: it is a
   * user-visible string, so the caller passes it from the dictionary
   * (DESIGN.md §7), the same contract `ErrorState` uses for its title.
   */
  emptyLabel: string;
  /** Rendered under the block when the server cut the patch off mid-diff. */
  truncatedNote?: string;
  className?: string;
}) {
  const lines = parseUnifiedDiff(patch);
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <div className="scroll-x max-h-[32rem] overflow-y-auto rounded-md border border-border bg-bg-sunken font-mono text-xs leading-relaxed">
        {lines.length === 0 ? (
          <p className="px-3 py-2 font-sans text-sm text-fg-subtle">{emptyLabel}</p>
        ) : (
          // `w-max min-w-full`: the rows are as wide as the longest line, so a
          // row tint runs the full scrollable width instead of stopping at the
          // viewport edge, and still fills the box when every line is short.
          <div className="w-max min-w-full">
            {lines.map((line, i) => (
              // The index is the identity here: two identical lines in a patch
              // are two different rows, and the list is never reordered.
              // biome-ignore lint/suspicious/noArrayIndexKey: a diff row has no id but its position
              <DiffRow key={i} line={line} />
            ))}
          </div>
        )}
      </div>
      {truncatedNote && <p className="text-xs font-medium text-warning-strong">{truncatedNote}</p>}
    </div>
  );
}
