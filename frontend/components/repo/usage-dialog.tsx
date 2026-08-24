"use client";

import { Terminal } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import { Dialog } from "@/components/ui/dialog";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { publicApiBase } from "@/lib/paths";
import { type UsageSnippetKind, usageEnv, usageSnippets } from "@/lib/usage-snippets";
import type { RepoKind } from "@/types/api";

/**
 * "Use this model / dataset": the snippets that make `huggingface_hub`,
 * `datasets` and `transformers` talk to this server.
 *
 * That the HF clients work unchanged against a `HF_ENDPOINT` pointed here is
 * the whole premise of the product, and before this the repository page
 * offered only `git clone` and a `gcloud storage` script — the words
 * `HF_ENDPOINT`, `load_dataset`, `from_pretrained` and `snapshot_download`
 * appeared nowhere in the UI at all.
 *
 * Sibling of `GcsAccessDialog` and deliberately shaped like it, with one
 * difference: nothing here is fetched. Every string is derived from the
 * repository already on the page plus the configured public API origin, so
 * there is no loading, empty or error state to render — the panel cannot be
 * in any state other than "here it is".
 */
const SNIPPET_LABEL: Record<UsageSnippetKind, MessageKey> = {
  datasets: "repo.usage.datasetsLabel",
  download: "repo.usage.downloadLabel",
  transformers: "repo.usage.transformersLabel",
};

export function UsageDialog({
  kind,
  ns,
  name,
  rev,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  /** The revision the reader is looking at, named in the "pin it" hint. */
  rev: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);

  const label = t(kind === "dataset" ? "repo.usage.labelDataset" : "repo.usage.labelModel");
  // The endpoint is never hard-coded: it is the same origin the browser uses
  // for downloads and resolve URLs, so a deployment behind another host name
  // hands out snippets that actually work.
  const env = usageEnv(publicApiBase());
  const snippets = usageSnippets(kind, `${ns}/${name}`);

  return (
    <>
      <Button variant="primary" onClick={() => setOpen(true)} className="w-full justify-center">
        <Terminal size={14} />
        {label}
      </Button>
      {open && (
        <Dialog open={open} onClose={() => setOpen(false)} title={label}>
          <div className="flex flex-col gap-4 px-4 py-4">
            <p className="text-sm text-fg-muted">{t("repo.usage.intro")}</p>

            <div className="flex flex-col gap-1.5">
              <CodeBlock
                value={env}
                label={t("repo.usage.envLabel")}
                copyLabel={t("repo.usage.copyEnv")}
              />
              <p className="text-xs font-medium text-fg-subtle">{t("repo.usage.envHint")}</p>
              <p className="text-xs font-medium text-fg-subtle">{t("repo.usage.tokenHint")}</p>
            </div>

            {snippets.map((snippet) => (
              <div key={snippet.kind} className="flex flex-col gap-1.5">
                <CodeBlock
                  value={snippet.code}
                  label={t(SNIPPET_LABEL[snippet.kind])}
                  copyLabel={t("repo.usage.copySnippet")}
                />
                {snippet.kind === "transformers" && (
                  <p className="text-xs font-medium text-fg-subtle">
                    {t("repo.usage.transformersHint")}
                  </p>
                )}
                {snippet.kind === "download" && (
                  <p className="text-xs font-medium text-fg-subtle">
                    {t("repo.usage.revisionHint", { rev })}
                  </p>
                )}
              </div>
            ))}
          </div>
        </Dialog>
      )}
    </>
  );
}
