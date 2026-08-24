"use client";

import { useState } from "react";
import { Slider } from "@/components/ui/field";
import { FileDropZone } from "@/components/ui/file-drop";
import { ProgressBar } from "@/components/ui/progress-bar";

/**
 * FileDropZone only reports what was picked, so the styleguide entry needs a
 * little state to show it — and the same page is the easiest place to see the
 * ProgressBar fill without starting a real upload.
 */
export function StyleguideFileDropDemo() {
  const [picked, setPicked] = useState<string[]>([]);
  const [progress, setProgress] = useState(0.35);

  return (
    <div className="flex flex-col gap-4">
      <FileDropZone
        onFiles={(files) => setPicked(files.map((f) => f.name))}
        label="Drop files here, or click to choose"
        hint="Nothing is uploaded from the styleguide."
        browseHint="Choose files"
      />
      <p className="text-xs font-medium text-fg-subtle">
        {picked.length === 0 ? "Nothing picked yet" : `Picked: ${picked.join(", ")}`}
      </p>
      <ProgressBar value={progress} label="Demo progress" />
      <Slider
        min={0}
        max={1}
        step={0.01}
        value={progress}
        aria-label="Demo progress value"
        onChange={(e) => setProgress(Number(e.target.value))}
      />
    </div>
  );
}
