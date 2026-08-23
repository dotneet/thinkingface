import {
  Box,
  File as FileIcon,
  FileImage,
  FileJson,
  FileSpreadsheet,
  FileText,
  Folder,
} from "lucide-react";
import type { TreeEntryUI } from "@/types/api";

export function EntryIcon({ entry, size = 16 }: { entry: TreeEntryUI; size?: number }) {
  if (entry.type === "directory") {
    return <Folder size={size} className="text-accent" />;
  }
  if (entry.is_parquet) {
    return <FileSpreadsheet size={size} className="text-fg-subtle" />;
  }
  switch (entry.preview) {
    case "image":
      return <FileImage size={size} className="text-fg-subtle" />;
    case "model":
      return <Box size={size} className="text-fg-subtle" />;
    case "markdown":
      return <FileText size={size} className="text-fg-subtle" />;
    case "text":
      if (entry.name.endsWith(".json")) {
        return <FileJson size={size} className="text-fg-subtle" />;
      }
      return <FileText size={size} className="text-fg-subtle" />;
    default:
      return <FileIcon size={size} className="text-fg-subtle" />;
  }
}
