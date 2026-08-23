"use client";

import { FileText, Table2 } from "lucide-react";
import { useState } from "react";
import { SegmentedControl } from "@/components/ui/segmented-control";

const OPTIONS = [
  { value: "table" as const, label: "Table", icon: Table2 },
  { value: "raw" as const, label: "Raw", icon: FileText },
];

export function StyleguideSegmentedDemo() {
  const [mode, setMode] = useState<"table" | "raw">("table");
  return (
    <div className="flex items-center gap-3">
      <SegmentedControl label="Demo mode" value={mode} options={OPTIONS} onChange={setMode} />
      <span className="text-xs font-medium text-fg-subtle">
        Selected: <code className="font-mono">{mode}</code>
      </span>
    </div>
  );
}
