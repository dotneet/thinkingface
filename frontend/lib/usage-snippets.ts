import type { RepoKind } from "@/types/api";

/**
 * The "use this repository" code snippets, built as plain strings so they can
 * be unit-tested and so the component that renders them stays a thin shell.
 *
 * The point of the product is that `huggingface_hub`, `datasets` and
 * `transformers` need no code changes — only `HF_ENDPOINT` — and until now the
 * UI said that nowhere. The copy here mirrors
 * `docs/users/guides/downloading.md`; keep the two in step.
 *
 * Framework-free on purpose (DESIGN.md §6's `client-boundary` rule): both a
 * Server and a Client Component may reach for it.
 */

/** Which client a snippet drives. The component maps this to a dictionary key. */
export type UsageSnippetKind = "datasets" | "download" | "transformers";

export type UsageSnippet = {
  kind: UsageSnippetKind;
  code: string;
};

/**
 * The environment every snippet assumes.
 *
 * `HF_HUB_DISABLE_XET` is not optional dressing: huggingface_hub >= 1.0
 * reaches for Xet whenever `hf_xet` is installed, and this server speaks LFS
 * only. Leaving it out is the most common way for a first attempt to fail.
 *
 * `HF_TOKEN` is deliberately absent — a token is per-user, so printing a
 * placeholder that looks like a value would invite pasting it verbatim. The
 * dialog says in prose that it may be needed.
 */
export function usageEnv(endpoint: string): string {
  return [`export HF_ENDPOINT=${endpoint}`, "export HF_HUB_DISABLE_XET=1"].join("\n");
}

/**
 * Snippets for one repository, most-likely-first: reading a dataset, or
 * loading a model. `repoId` is the bare "namespace/name" that every HF client
 * calls `repo_id` — never a URL.
 */
export function usageSnippets(kind: RepoKind, repoId: string): UsageSnippet[] {
  if (kind === "dataset") {
    return [
      {
        kind: "datasets",
        code: ["from datasets import load_dataset", "", `ds = load_dataset("${repoId}")`].join(
          "\n",
        ),
      },
      {
        kind: "download",
        code: [
          "from huggingface_hub import snapshot_download",
          "",
          `local_dir = snapshot_download(repo_id="${repoId}", repo_type="dataset")`,
        ].join("\n"),
      },
    ];
  }
  return [
    {
      kind: "transformers",
      code: [
        "from transformers import AutoModel, AutoTokenizer",
        "",
        `tokenizer = AutoTokenizer.from_pretrained("${repoId}")`,
        `model = AutoModel.from_pretrained("${repoId}")`,
      ].join("\n"),
    },
    {
      kind: "download",
      code: [
        "from huggingface_hub import snapshot_download",
        "",
        `local_dir = snapshot_download(repo_id="${repoId}")`,
      ].join("\n"),
    },
  ];
}
