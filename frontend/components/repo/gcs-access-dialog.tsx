"use client";

import { Cloud } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { errorMessage, type FailedApiResult } from "@/lib/api-error-message";
import { formatBytes } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { getRepoGCS } from "@/lib/repos";
import type { RepoGCSResponse, RepoKind } from "@/types/api";

/**
 * Replaces the always-visible `gcloud storage` block that used to read
 * `RepoDetail.gcloud_command`. That field is gone (exports/ was retired --
 * bucket keys are now content-addressed and independent of namespace/repo
 * name), so the script has to be built from the live file listing instead.
 * Walking every indexed file of a revision isn't free, so this fetches
 * `GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}` lazily, the first time the
 * dialog opens, and caches the result for the rest of the component's life.
 */
export function GcsAccessDialog({
  kind,
  ns,
  name,
  rev,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<RepoGCSResponse | null>(null);
  const [error, setError] = useState<FailedApiResult | null>(null);
  const [loading, setLoading] = useState(false);

  function handleOpen() {
    setOpen(true);
    if (loading) return;
    // Fetched on every open rather than cached: the sync worker may have
    // indexed the revision since the last look, and an empty listing from
    // before that would otherwise stick until the page remounts.
    setLoading(true);
    setError(null);
    getRepoGCS(kind, ns, name, rev).then((result) => {
      setLoading(false);
      if (!result.ok) {
        setError(result);
        return;
      }
      setData(result.data);
    });
  }

  const totalSize = data ? data.files.reduce((sum, f) => sum + f.size, 0) : 0;

  return (
    <>
      <Button onClick={handleOpen} className="w-full justify-center">
        <Cloud size={14} />
        {t("repo.sidebar.gcsAccess.label")}
      </Button>
      {open && (
        <Dialog
          open={open}
          onClose={() => setOpen(false)}
          title={t("repo.sidebar.gcsAccess.label")}
        >
          <div className="flex flex-col gap-4 px-4 py-4">
            {loading && <SkeletonLines lines={5} />}

            {!loading && error && (
              <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, error)} />
            )}

            {!loading && !error && data && data.files.length === 0 && (
              <EmptyState
                icon={Cloud}
                title={t("repo.sidebar.gcsAccess.emptyTitle")}
                description={t("repo.sidebar.gcsAccess.emptyDescription")}
              />
            )}

            {!loading && !error && data && data.files.length > 0 && (
              <>
                <p className="text-sm text-fg-muted">
                  {t(
                    data.files.length === 1
                      ? "repo.sidebar.gcsAccess.summaryOne"
                      : "repo.sidebar.gcsAccess.summaryOther",
                    { count: data.files.length, size: formatBytes(totalSize) },
                  )}
                </p>

                <div className="flex flex-col gap-2">
                  <CodeBlock
                    value={data.gcloud_script}
                    label={t("repo.sidebar.gcsAccess.scriptLabel")}
                    copyLabel={t("repo.sidebar.gcsAccess.copyScript")}
                    maxHeight="max-h-64"
                  />
                  <p className="text-xs font-medium text-fg-subtle">
                    {t("repo.sidebar.gcsAccess.destHint")}
                  </p>
                </div>

                {data.duckdb_snippet && (
                  <CodeBlock
                    value={data.duckdb_snippet}
                    label={t("repo.sidebar.gcsAccess.duckdbLabel")}
                    copyLabel={t("repo.sidebar.gcsAccess.copyDuckdb")}
                    maxHeight="max-h-48"
                  />
                )}
              </>
            )}
          </div>
        </Dialog>
      )}
    </>
  );
}
