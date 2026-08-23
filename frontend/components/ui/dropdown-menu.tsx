"use client";

import { useRef, useState } from "react";
import { useOnClickOutside } from "@/hooks/use-on-click-outside";
import { cn } from "@/lib/cn";

/**
 * Generic trigger + floating panel. The trigger is a render prop so callers
 * can style their own button (icon-only, text label, ...); the panel is a
 * render prop too, receiving `close` so an item's onClick can dismiss the
 * menu after acting (see components/repo/ref-switcher.tsx for the pattern
 * this follows, copied from user-menu.tsx before this primitive existed).
 */
export function DropdownMenu({
  trigger,
  children,
  align = "start",
  className,
}: {
  trigger: (props: {
    open: boolean;
    toggle: () => void;
    /** Spread onto the trigger element so it announces itself correctly. */
    triggerProps: { "aria-expanded": boolean; "aria-haspopup": "menu" };
  }) => React.ReactNode;
  children: (props: { close: () => void }) => React.ReactNode;
  align?: "start" | "end";
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const close = () => setOpen(false);
  useOnClickOutside(ref, close);

  return (
    <div className="relative inline-block" ref={ref}>
      {trigger({
        open,
        toggle: () => setOpen((v) => !v),
        triggerProps: { "aria-expanded": open, "aria-haspopup": "menu" },
      })}
      {open && (
        <div
          className={cn(
            "absolute top-full z-10 mt-1 max-h-80 min-w-[14rem] overflow-y-auto rounded-lg border border-border bg-bg-raised p-1 shadow-lg",
            align === "end" ? "right-0" : "left-0",
            className,
          )}
        >
          {children({ close })}
        </div>
      )}
    </div>
  );
}

export function DropdownMenuLabel({ children }: { children: React.ReactNode }) {
  return <div className="px-3 py-1.5 text-xs font-medium text-fg-subtle">{children}</div>;
}

export function DropdownMenuSeparator() {
  return <div className="my-1 h-px bg-border" />;
}

export function DropdownMenuItem({
  active,
  className,
  children,
  ...props
}: React.ComponentProps<"button"> & { active?: boolean }) {
  return (
    <button
      type="button"
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-left text-sm text-fg-muted transition-colors hover:bg-bg-hover hover:text-fg",
        active && "bg-accent-muted text-accent-strong hover:bg-accent-muted",
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}
