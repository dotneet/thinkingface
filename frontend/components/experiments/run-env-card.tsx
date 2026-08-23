"use client";

import { Server } from "lucide-react";
import { ConfigEntryTable } from "@/components/experiments/config-entry-table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { useT } from "@/lib/i18n/client";
import { type ConfigEntry, isEnvEmpty, runEnv } from "@/lib/run-config";

/**
 * The run's execution environment: what the trackio shim snapshots into
 * `config["_meta"]` at `init()` (git state, command line, interpreter, host,
 * GPU, a hash of the installed packages).
 *
 * It gets its own section rather than sitting in the hyperparameter table
 * because it answers a different question — "can I reproduce this run?" — and
 * because the fields are known in advance, so they can be labelled and laid
 * out instead of listed as opaque dotted keys.
 */
export function RunEnvCard({ meta }: { meta: ConfigEntry[] }) {
  const t = useT();
  const env = runEnv(meta);

  if (isEnvEmpty(env)) {
    return (
      <EmptyState
        icon={Server}
        title={t("experiments.env.emptyTitle")}
        description={t("experiments.env.emptyDescription")}
      />
    );
  }

  const rows: { label: string; value: React.ReactNode }[] = [];
  if (env.gitCommit !== undefined) {
    rows.push({
      label: t("experiments.env.gitCommit"),
      value: (
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs break-all">{env.gitCommit}</span>
          {env.gitDirty !== undefined && (
            <Badge tone={env.gitDirty ? "warning" : "positive"}>
              {env.gitDirty ? t("experiments.env.dirty") : t("experiments.env.clean")}
            </Badge>
          )}
        </span>
      ),
    });
  }
  if (env.gitBranch !== undefined) {
    rows.push({ label: t("experiments.env.gitBranch"), value: env.gitBranch });
  }
  if (env.cmdline !== undefined) {
    rows.push({
      label: t("experiments.env.cmdline"),
      value: (
        <code className="block font-mono text-xs break-all whitespace-pre-wrap">{env.cmdline}</code>
      ),
    });
  }
  if (env.python !== undefined) {
    rows.push({ label: t("experiments.env.python"), value: env.python });
  }
  if (env.platform !== undefined) {
    rows.push({ label: t("experiments.env.platform"), value: env.platform });
  }
  if (env.hostname !== undefined) {
    rows.push({ label: t("experiments.env.hostname"), value: env.hostname });
  }
  if (env.gpuName !== undefined || env.gpuCount !== undefined) {
    const name = env.gpuName ?? t("experiments.env.gpuUnknown");
    rows.push({
      label: t("experiments.env.gpu"),
      value:
        env.gpuCount !== undefined
          ? t("experiments.env.gpuValue", { name, count: env.gpuCount })
          : name,
    });
  }
  if (env.cuda !== undefined) {
    rows.push({ label: t("experiments.env.cuda"), value: env.cuda });
  }
  if (env.requirementsSha256 !== undefined) {
    rows.push({
      label: t("experiments.env.requirements"),
      value: (
        <span className="font-mono text-xs break-all" title={env.requirementsSha256}>
          {env.requirementsSha256}
        </span>
      ),
    });
  }

  return (
    <div className="flex flex-col gap-4">
      {rows.length > 0 && (
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 rounded-lg border border-border bg-bg-raised p-4 sm:grid-cols-[max-content_1fr]">
          {rows.map((row) => (
            // The label/value pair is one grid row on wide screens and two
            // stacked lines when the grid collapses to a single column.
            <div key={row.label} className="contents">
              <dt className="text-sm font-medium text-fg-muted">{row.label}</dt>
              <dd className="min-w-0 text-sm text-fg">{row.value}</dd>
            </div>
          ))}
        </dl>
      )}

      {env.extra.length > 0 && (
        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-semibold">{t("experiments.env.otherTitle")}</h3>
          <ConfigEntryTable entries={env.extra} emptyTitle={t("experiments.env.emptyTitle")} />
        </div>
      )}
    </div>
  );
}
