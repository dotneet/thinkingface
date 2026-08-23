"use client";

import { useQuery } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { DTypeBreakdown } from "@/components/model/model-dtype-breakdown";
import { ModelInspectorNotes } from "@/components/model/model-inspector-notes";
import { ModelInspectorSkeleton } from "@/components/model/model-inspector-skeleton";
import { MetadataTable } from "@/components/model/model-metadata-table";
import { SummaryRow } from "@/components/model/model-summary-row";
import { TensorTable } from "@/components/model/model-tensor-table";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { getModelMeta } from "@/lib/model-meta";
import type { RepoKind } from "@/types/api";

export function ModelInspector({
  kind,
  ns,
  name,
  rev,
  path,
  size,
  downloadUrl,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
  size: number;
  downloadUrl: string;
}) {
  const t = useT();
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["model-meta", kind, ns, name, rev, path.join("/")],
    queryFn: async () => {
      const result = await getModelMeta(kind, ns, name, rev, path);
      // ApiResultError (not a bare Error) so the catch below can translate
      // the backend's `error.type` instead of showing raw English ([S12]).
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    // Metadata parsing can be slow on huge checkpoints, so this query is
    // only ever kicked off client-side (see file-preview.tsx) — the blob
    // page itself must never wait on it.
    staleTime: 60_000,
  });

  if (isLoading) {
    return <ModelInspectorSkeleton />;
  }

  if (isError || !data) {
    return (
      <ErrorState
        title={t("ui.errorStateTitle")}
        message={queryErrorMessage(t, error, t("model.inspector.loadFailed"))}
        action={
          <a href={downloadUrl} className={buttonClass({ variant: "primary" })}>
            <Download size={14} />
            {t("model.inspector.download")}
          </a>
        }
      />
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <SummaryRow
        format={data.format}
        numParameters={data.num_parameters}
        numTensors={data.num_tensors}
        size={size}
      />

      {data.dtypes.length > 0 && <DTypeBreakdown dtypes={data.dtypes} />}

      {Object.keys(data.metadata).length > 0 && <MetadataTable metadata={data.metadata} />}

      <TensorTable tensors={data.tensors} />

      <ModelInspectorNotes
        truncated={data.truncated}
        shownTensorCount={data.tensors.length}
        warnings={data.warnings}
      />
    </div>
  );
}
