import { CopyButton } from "@/components/ui/copy-button";
import { cn } from "@/lib/cn";

/**
 * A monospace block for a shell command, script or query, with a copy
 * affordance. Two layouts, chosen by whether `label` is given:
 *
 * - with `label`: an uppercase label row (metadata style) above the block,
 *   with an icon-only copy button on the right of the label.
 * - without `label`: no label row; the copy button sits inside the block
 *   itself, next to the text.
 */
export function CodeBlock({
  value,
  label,
  copyLabel,
  maxHeight,
  className,
}: {
  value: string;
  label?: string;
  /** Accessible name / tooltip for the copy button. Defaults to "Copy". */
  copyLabel?: string;
  /**
   * A Tailwind max-height utility (e.g. `"max-h-64"`). When set, the block
   * scrolls vertically instead of growing — for long scripts.
   */
  maxHeight?: string;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      {label && (
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
            {label}
          </span>
          <CopyButton value={value} label={copyLabel} iconOnly />
        </div>
      )}
      <div
        className={cn(
          "flex items-start justify-between gap-2 rounded-md border border-border bg-bg-sunken p-2.5",
          maxHeight && cn(maxHeight, "overflow-y-auto"),
        )}
      >
        <code className="scroll-x whitespace-pre font-mono text-xs leading-relaxed text-fg-muted">
          {value}
        </code>
        {!label && <CopyButton value={value} label={copyLabel} />}
      </div>
    </div>
  );
}
