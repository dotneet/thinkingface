import type { LucideIcon } from "lucide-react";

/**
 * One block of the run page's "Outputs & environment" card: artifacts, the
 * models the run produced, the environment snapshot.
 *
 * Empty is the common case for all three, and three full `EmptyState`s used to
 * take ~600px to say "nothing here". An empty block is now one line — title,
 * then the hint in small muted text (and as a tooltip once it truncates) — and
 * only a block with content grows to show it.
 */
export function RunPageOutputRow({
  icon: Icon,
  title,
  description,
  empty,
  count,
  children,
}: {
  icon: LucideIcon;
  title: string;
  /** What the block is, as the title's tooltip. */
  description?: string;
  /** The block has nothing to show: this hint is the whole row. */
  empty?: string;
  /** Shown after the title, e.g. how many artifacts there are. */
  count?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-2 px-4 py-3">
      <div className="flex min-w-0 items-center gap-2 text-sm">
        <Icon size={15} strokeWidth={1.5} className="shrink-0 text-fg-subtle" />
        <h3 className="shrink-0 font-semibold" title={description}>
          {title}
        </h3>
        {count !== undefined && (
          <span className="shrink-0 text-xs font-medium tabular-nums text-fg-subtle">{count}</span>
        )}
        {empty !== undefined && (
          <span className="min-w-0 truncate text-xs font-medium text-fg-subtle" title={empty}>
            {empty}
          </span>
        )}
      </div>
      {empty === undefined && children}
    </div>
  );
}
