"use client";

import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

export type SegmentedOption<T extends string> = {
  value: T;
  label: string;
  icon?: LucideIcon;
  disabled?: boolean;
};

/**
 * Small in-page mode switch (Rows / SQL, Table / Raw).
 *
 * Distinct from RepoTabs, which navigates: nothing here changes the URL, so
 * these are buttons in a group rather than links or ARIA tabs — the panels
 * they reveal are plain content, not tabpanels tied to a roving tabindex. The
 * group is a bare <fieldset> (no legend, labelled by `aria-label`) because
 * that is the native element for "these controls belong together"; `<div
 * role="group">` would say the same thing the long way round.
 */
export function SegmentedControl<T extends string>({
  value,
  options,
  onChange,
  label,
  className,
}: {
  value: T;
  options: SegmentedOption<T>[];
  onChange: (value: T) => void;
  /** Accessible name for the group, e.g. "Viewer mode". */
  label: string;
  className?: string;
}) {
  return (
    <fieldset
      aria-label={label}
      // min-w-0 undoes the UA's `min-inline-size: min-content` on fieldset,
      // which would otherwise refuse to shrink inside a flex row.
      className={cn("inline-flex min-w-0 gap-1 rounded-lg border border-border p-1", className)}
    >
      {options.map((option) => {
        const Icon = option.icon;
        const selected = option.value === value;
        return (
          <Button
            key={option.value}
            size="sm"
            variant={selected ? "primary" : "ghost"}
            aria-pressed={selected}
            disabled={option.disabled}
            onClick={() => onChange(option.value)}
          >
            {Icon && <Icon size={13} />}
            {option.label}
          </Button>
        );
      })}
    </fieldset>
  );
}
