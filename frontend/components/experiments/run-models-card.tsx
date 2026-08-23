"use client";

import { Boxes, Link2Off } from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { expRunModelHref } from "@/lib/experiments";
import { useT } from "@/lib/i18n/client";
import type { ExpRunModelRef } from "@/types/api";

/**
 * The models a run declared it produced (`trackio.log_model`).
 *
 * A recorded model whose repository is not on this server — a typo, a push
 * that never happened, or a repository that was never pushed — is
 * kept and shown as text with a note, never dropped and never linked. That is
 * the same treatment a dangling `lineage:` reference gets
 * (docs/dev/api-contract.md §12), so the two halves of the provenance UI behave
 * alike.
 *
 * The pinned revision is recorded verbatim and is not verified: a link to a
 * revision that has since been rewritten lands on the file browser's own
 * error rather than being hidden here.
 */
export function RunModelsCard({ models }: { models: ExpRunModelRef[] }) {
  const t = useT();

  if (models.length === 0) {
    return (
      <EmptyState
        icon={Boxes}
        title={t("experiments.models.emptyTitle")}
        description={t("experiments.models.emptyDescription")}
      />
    );
  }

  return (
    <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-lg border border-border">
      {models.map((model) => (
        <li
          key={`${model.repo_id}@${model.revision}`}
          className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm"
        >
          <Boxes size={15} strokeWidth={1.5} className="shrink-0 text-fg-subtle" />
          <ModelLink model={model} notFound={t("experiments.models.notFound")} />
          {model.revision && (
            <Badge>
              <span className="font-mono">{model.revision.slice(0, 12)}</span>
            </Badge>
          )}
        </li>
      ))}
    </ul>
  );
}

function ModelLink({ model, notFound }: { model: ExpRunModelRef; notFound: string }) {
  const href = expRunModelHref(model);

  if (href === null) {
    return (
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
        <span className="font-mono text-xs break-all text-fg-muted">{model.repo_id}</span>
        <span className="flex items-center gap-1 text-xs font-medium text-fg-subtle">
          <Link2Off size={11} strokeWidth={1.5} />
          {notFound}
        </span>
      </span>
    );
  }
  return (
    <Link
      href={href}
      className="min-w-0 flex-1 font-mono text-xs break-all text-accent hover:underline"
    >
      {model.repo_id}
    </Link>
  );
}
