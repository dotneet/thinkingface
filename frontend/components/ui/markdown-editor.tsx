"use client";

import { useDeferredValue, useEffect, useRef, useState } from "react";
import { Textarea } from "@/components/ui/field";
import { Markdown, type MarkdownProps } from "@/components/ui/markdown";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";

type Mode = "edit" | "preview" | "split";

const STORAGE_KEY = "tf.markdownEditor.mode";
// Matches Tailwind's default `lg` breakpoint, which is also what gates the
// split option in the SegmentedControl and the split-view grid below.
const SPLIT_MEDIA_QUERY = "(min-width: 1024px)";

/**
 * Shared Markdown edit surface: a textarea with edit / preview / split modes,
 * used by the repo file editor and (in a later pass) the run-note editor.
 *
 * Preview rendering goes through `Markdown` (components/ui/markdown.tsx) so
 * every editor previews Markdown identically to the rest of the app.
 */
export function MarkdownEditor({
  value,
  onChange,
  onSubmit,
  placeholder,
  ariaLabel,
  minHeightClassName = "min-h-[60vh]",
  markdown = true,
  previewProps,
  disabled,
  autoFocus,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  /** Called on ⌘/Ctrl+Enter, e.g. to submit the surrounding form. */
  onSubmit?: () => void;
  placeholder?: string;
  ariaLabel: string;
  minHeightClassName?: string;
  /** false renders a plain textarea only, for non-Markdown files. */
  markdown?: boolean;
  previewProps?: Pick<
    MarkdownProps,
    "assetBaseUrl" | "repoRootUrl" | "linkContext" | "stripFrontmatter"
  >;
  disabled?: boolean;
  /** Focus the textarea on mount — for editors opened specifically to write. */
  autoFocus?: boolean;
  className?: string;
}) {
  const t = useT();
  const [mode, setMode] = useState<Mode>("edit");
  const [canSplit, setCanSplit] = useState(false);

  const previewRef = useRef<HTMLDivElement>(null);

  // Preview rendering can be expensive on a large file; defer it behind the
  // (synchronous) textarea updates so typing never feels blocked.
  const deferredValue = useDeferredValue(value);

  // Restore the persisted mode after mount, not during the initial render,
  // so server and client markup match on first paint.
  useEffect(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === "edit" || stored === "preview" || stored === "split") {
      setMode(stored);
    }
  }, []);

  // Split is only offered at `lg` and above; fall back to edit if the
  // viewport shrinks (or the persisted mode was split) below that.
  useEffect(() => {
    const mq = window.matchMedia(SPLIT_MEDIA_QUERY);
    function apply(matches: boolean) {
      setCanSplit(matches);
      setMode((current) => (current === "split" && !matches ? "edit" : current));
    }
    apply(mq.matches);
    function handleChange(e: MediaQueryListEvent) {
      apply(e.matches);
    }
    mq.addEventListener("change", handleChange);
    return () => mq.removeEventListener("change", handleChange);
  }, []);

  function selectMode(next: Mode) {
    setMode(next);
    localStorage.setItem(STORAGE_KEY, next);
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Tab") {
      e.preventDefault();
      const textarea = e.currentTarget;
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      onChange(`${value.slice(0, start)}  ${value.slice(end)}`);
      // Restore the cursor after the insertion once React re-renders the
      // (controlled) textarea with the new value.
      requestAnimationFrame(() => {
        textarea.selectionStart = textarea.selectionEnd = start + 2;
      });
      return;
    }
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      onSubmit?.();
    }
  }

  // One-way ratio sync (textarea -> preview) so scrolling either pane can
  // never fight the other — there's nothing feeding back from the preview
  // side to create a loop.
  function handleTextareaScroll(e: React.UIEvent<HTMLTextAreaElement>) {
    if (mode !== "split") return;
    const textarea = e.currentTarget;
    const preview = previewRef.current;
    if (!preview) return;
    const range = textarea.scrollHeight - textarea.clientHeight;
    const ratio = range > 0 ? textarea.scrollTop / range : 0;
    preview.scrollTop = ratio * (preview.scrollHeight - preview.clientHeight);
  }

  const lineCount = value === "" ? 0 : value.split("\n").length;

  function renderPreview(extraClassName?: string) {
    return (
      <div
        ref={previewRef}
        className={cn(
          "overflow-y-auto rounded-lg border border-border bg-bg-raised p-6",
          extraClassName,
        )}
      >
        {deferredValue.trim() ? (
          <Markdown source={deferredValue} {...previewProps} />
        ) : (
          <p className="text-sm text-fg-subtle">{t("ui.markdownEditor.nothingToPreview")}</p>
        )}
      </div>
    );
  }

  function renderTextarea(extraClassName?: string) {
    return (
      <Textarea
        autoFocus={autoFocus}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={handleKeyDown}
        onScroll={handleTextareaScroll}
        placeholder={placeholder}
        disabled={disabled}
        spellCheck={false}
        className={cn("resize-none font-mono text-sm leading-relaxed", extraClassName)}
        aria-label={ariaLabel}
      />
    );
  }

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      {markdown && (
        <SegmentedControl
          value={mode}
          onChange={selectMode}
          label={t("ui.markdownEditor.modeLabel")}
          options={[
            { value: "edit" as const, label: t("ui.markdownEditor.modeEdit") },
            { value: "preview" as const, label: t("ui.markdownEditor.modePreview") },
            ...(canSplit
              ? [{ value: "split" as const, label: t("ui.markdownEditor.modeSplit") }]
              : []),
          ]}
        />
      )}

      {markdown && mode === "split" ? (
        <div className="grid gap-4 lg:grid-cols-2">
          {renderTextarea(cn(minHeightClassName, "max-h-[70vh]"))}
          {renderPreview(cn(minHeightClassName, "max-h-[70vh]"))}
        </div>
      ) : markdown && mode === "preview" ? (
        renderPreview(minHeightClassName)
      ) : (
        renderTextarea(minHeightClassName)
      )}

      <div className="flex justify-end text-xs font-medium text-fg-subtle">
        {t("ui.markdownEditor.status", { lines: lineCount, chars: value.length })}
      </div>
    </div>
  );
}
