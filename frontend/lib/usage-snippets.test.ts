import { describe, expect, it } from "vitest";
import { usageEnv, usageSnippets } from "@/lib/usage-snippets";

describe("usageEnv", () => {
  it("points the HF clients at the given endpoint", () => {
    expect(usageEnv("http://localhost:8080")).toContain("export HF_ENDPOINT=http://localhost:8080");
  });

  // huggingface_hub >= 1.0 prefers Xet whenever hf_xet is installed, and this
  // server speaks LFS only — a snippet without this line fails for anyone who
  // has the package.
  it("disables Xet", () => {
    expect(usageEnv("https://hub.example.com")).toContain("export HF_HUB_DISABLE_XET=1");
  });

  // A token is per-user; printing a placeholder invites pasting it verbatim.
  it("never invents a token value", () => {
    expect(usageEnv("http://localhost:8080")).not.toContain("HF_TOKEN");
  });
});

describe("usageSnippets", () => {
  it("leads with load_dataset for a dataset", () => {
    const snippets = usageSnippets("dataset", "admin/imdb-reviews");
    expect(snippets.map((s) => s.kind)).toEqual(["datasets", "download"]);
    expect(snippets[0]?.code).toContain('load_dataset("admin/imdb-reviews")');
  });

  it("passes repo_type for a dataset snapshot", () => {
    const download = usageSnippets("dataset", "admin/imdb-reviews").find(
      (s) => s.kind === "download",
    );
    expect(download?.code).toContain('repo_type="dataset"');
  });

  it("leads with from_pretrained for a model", () => {
    const snippets = usageSnippets("model", "acme/sentiment-base");
    expect(snippets.map((s) => s.kind)).toEqual(["transformers", "download"]);
    expect(snippets[0]?.code).toContain('AutoModel.from_pretrained("acme/sentiment-base")');
  });

  // repo_type defaults to "model", and passing it would only be noise.
  it("omits repo_type for a model snapshot", () => {
    const download = usageSnippets("model", "acme/sentiment-base").find(
      (s) => s.kind === "download",
    );
    expect(download?.code).toContain('snapshot_download(repo_id="acme/sentiment-base")');
    expect(download?.code).not.toContain("repo_type");
  });

  // The clients take a bare "namespace/name", never a URL.
  it("uses the bare repo id, not a URL", () => {
    for (const snippet of usageSnippets("model", "acme/sentiment-base")) {
      expect(snippet.code).not.toContain("http");
    }
  });
});
