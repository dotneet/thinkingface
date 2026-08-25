"use client";

import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";

/**
 * Saves a CSV the page already has in hand.
 *
 * No request is involved: everything these pages export is data they are
 * already rendering, so the file is built in the tab and handed to the browser
 * through a blob URL. The URL is revoked on the next frame — an object URL
 * pins its blob in memory until it is released, and a few thousand exported
 * rows held for the lifetime of the tab is exactly the leak nobody notices.
 *
 * `build` is a thunk, not a string, for the same reason CopyButton's `value`
 * is (components/ui/copy-button.tsx): serialising thousands of rows should
 * happen on the click, not on every render of the page around it.
 */
export function CsvDownloadButton({
  filename,
  build,
  label,
  disabled,
}: {
  filename: string;
  build: () => string;
  label: string;
  disabled?: boolean;
}) {
  function handleClick() {
    const blob = new Blob([build()], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = filename;
    anchor.click();
    // Not revoked synchronously: Safari has historically cancelled the save if
    // the URL disappears in the same task as the click.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  return (
    <Button size="sm" variant="secondary" onClick={handleClick} disabled={disabled}>
      <Download size={14} />
      {label}
    </Button>
  );
}
