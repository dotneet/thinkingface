"use client";

import { Upload } from "lucide-react";
import { useRef, useState } from "react";
import { cn } from "@/lib/cn";

/**
 * The file picker. A `<input type="file">` cannot be styled, so the real
 * control is visually hidden (`sr-only` keeps it focusable and keyboard
 * operable) and the `<label>` around it is what the user sees — which also
 * makes the whole drop area a click target for free, with no
 * `ref.current.click()` anywhere.
 *
 * It lives in `ui/` rather than in the upload dialog because it is the only
 * place in the app allowed to write `type="file"`: everything else asks for
 * files through this component (DESIGN.md §5).
 */
export function FileDropZone({
  onFiles,
  label,
  hint,
  browseHint,
  multiple = true,
  disabled = false,
  className,
}: {
  /** Called with everything the user picked or dropped; never with an empty list. */
  onFiles: (files: File[]) => void;
  label: string;
  /** Secondary line under the label — where the files will land, say. */
  hint?: string;
  /** Accessible name for the hidden input itself. */
  browseHint: string;
  multiple?: boolean;
  disabled?: boolean;
  className?: string;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);

  function emit(list: FileList | null) {
    const files = Array.from(list ?? []);
    if (files.length > 0) onFiles(files);
  }

  return (
    <label
      onDragOver={(e) => {
        // preventDefault before the disabled check, not after: without it the
        // browser navigates to the dropped file, and being disabled is
        // exactly when that hurts most -- an upload is in flight, so the
        // navigation aborts the XHR and strands whatever it had staged.
        e.preventDefault();
        if (disabled) return;
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => {
        e.preventDefault();
        if (disabled) return;
        setDragging(false);
        emit(e.dataTransfer?.files ?? null);
      }}
      className={cn(
        "flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center transition-colors",
        dragging ? "border-accent bg-accent-muted" : "border-border-control bg-bg-sunken",
        disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer hover:border-accent",
        className,
      )}
    >
      <Upload size={28} strokeWidth={1.5} className="text-fg-subtle" />
      <span className="text-sm font-medium text-fg">{label}</span>
      {hint && <span className="text-xs font-medium text-fg-subtle">{hint}</span>}
      <input
        ref={inputRef}
        type="file"
        className="sr-only"
        multiple={multiple}
        disabled={disabled}
        aria-label={browseHint}
        onChange={(e) => {
          emit(e.target.files);
          // Reset, or picking the same file twice in a row fires no change
          // event the second time and the dialog looks frozen.
          e.target.value = "";
        }}
      />
    </label>
  );
}
