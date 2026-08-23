import { type ApiResult, apiFetch } from "@/lib/api";
import { expRunHref } from "@/lib/experiments";
import { repoBase, repoTreeHref } from "@/lib/paths";
import type { FetchOpts } from "@/lib/repos";
import type {
  ExpLineageResponse,
  LineageDependent,
  LineageRef,
  RepoKind,
  RepoLineageResponse,
} from "@/types/api";

/**
 * Lineage of one repository: the datasets, base model and run its card
 * declares (upstream), and the repositories whose cards point back at it
 * (downstream).
 */
export function getRepoLineage(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<RepoLineageResponse>> {
  return apiFetch<RepoLineageResponse>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/lineage`,
    { headers: opts?.headers },
  );
}

/**
 * Repositories produced by the runs of one experiment project. Omit `run` to
 * get every run of the project in a single request; pass one to ask about it
 * alone (the answer then always contains that run, possibly with no models).
 */
export function getExperimentLineage(
  ns: string,
  repo: string,
  project: string,
  params?: { run?: string },
  opts?: FetchOpts,
): Promise<ApiResult<ExpLineageResponse>> {
  return apiFetch<ExpLineageResponse>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/lineage`,
    { query: { run: params?.run }, headers: opts?.headers },
  );
}

/** Models produced by each run, keyed by run name. */
export type RunModels = Record<string, LineageDependent[]>;

export function toRunModels(res: ExpLineageResponse): RunModels {
  const out: RunModels = {};
  for (const item of res.items) out[item.run] = item.models;
  return out;
}

/** Group upstream edges by kind, preserving the order the card wrote them in. */
export function groupUpstream(refs: LineageRef[]): {
  datasets: LineageRef[];
  baseModels: LineageRef[];
  evalDatasets: LineageRef[];
  runs: LineageRef[];
} {
  return {
    datasets: refs.filter((r) => r.kind === "dataset"),
    baseModels: refs.filter((r) => r.kind === "base_model"),
    // Evaluated on, not trained from: the same target kind, a different claim.
    evalDatasets: refs.filter((r) => r.kind === "eval_dataset"),
    runs: refs.filter((r) => r.kind === "run"),
  };
}

/**
 * The relations a model can have to its base model, in the order the model
 * tree lists them (HuggingFace's `base_model_relation`; see
 * docs/dev/api-contract.md §12).
 */
export const LINEAGE_RELATIONS = ["finetune", "adapter", "quantized", "merge"] as const;

export type KnownLineageRelation = (typeof LINEAGE_RELATIONS)[number];

/**
 * The bucket a dependent is filed under:
 *
 * - one of LINEAGE_RELATIONS, when the card declared or the server inferred it;
 * - `"other"` for a relation outside that set -- a card may write anything,
 *   and the server carries it through rather than rewriting it;
 * - `"new_version"` for a repository this one supersedes, which is the reverse
 *   of its own `new_version:` edge and belongs on its own;
 * - `"eval_dataset"` for a repository that only *evaluated* on this dataset;
 * - `""` for a dependent that has no relation at all (a dataset or run edge),
 *   which is the flat "derived from this" list.
 */
export type DependentBucket = KnownLineageRelation | "other" | "new_version" | "eval_dataset" | "";

export type DependentGroup = {
  bucket: DependentBucket;
  items: LineageDependent[];
};

function isKnownRelation(value: string): value is KnownLineageRelation {
  return (LINEAGE_RELATIONS as readonly string[]).includes(value);
}

/**
 * The bucket one dependent belongs in. Only a base model edge carries a
 * relation; an empty one on such an edge is read as "finetune", both because
 * that is the Hub's own default and because rows indexed before the relation
 * column existed have nothing else to say.
 */
export function dependentBucket(d: LineageDependent): DependentBucket {
  if (d.kind === "new_version" || d.kind === "eval_dataset") return d.kind;
  if (d.kind !== "base_model") return "";
  if (!d.relation) return "finetune";
  return isKnownRelation(d.relation) ? d.relation : "other";
}

/**
 * Group the downstream repositories into the model tree: the versions this
 * one supersedes first (they answer "where did this come from?" before
 * anything else does), then fine-tunes, adapters, quantizations, merges,
 * anything else, the repositories that only evaluated on it, and finally the
 * dependents that have no relation at all.
 *
 * Empty buckets are dropped, and the order within a bucket is the order the
 * server returned (most recently updated first).
 */
export function groupDependents(dependents: LineageDependent[]): DependentGroup[] {
  const order: DependentBucket[] = [
    "new_version",
    ...LINEAGE_RELATIONS,
    "other",
    "eval_dataset",
    "",
  ];
  const byBucket = new Map<DependentBucket, LineageDependent[]>();
  for (const d of dependents) {
    const bucket = dependentBucket(d);
    const items = byBucket.get(bucket);
    if (items) items.push(d);
    else byBucket.set(bucket, [d]);
  }
  return order
    .map((bucket) => ({ bucket, items: byBucket.get(bucket) ?? [] }))
    .filter((g) => g.items.length > 0);
}

/** Path to an upstream target, or null when it does not resolve (dangling). */
export function lineageRefHref(ref: LineageRef): string | null {
  if (!ref.exists) return null;
  if (ref.kind === "run") {
    // A run edge names one run, so it opens that run's own page rather than
    // the project it lives in -- the card said "this model came from *this*
    // run", and landing on a list of forty is a worse answer. A card that
    // named a project without a run still lands on the project page.
    return ref.run
      ? expRunHref(ref.namespace, ref.name, ref.project, ref.run)
      : `/experiments/${encodeURIComponent(ref.namespace)}/${encodeURIComponent(ref.name)}/${encodeURIComponent(ref.project)}`;
  }
  // A pinned revision opens the file browser at that revision; without one the
  // overview page is the better landing spot.
  return ref.rev
    ? repoTreeHref(ref.target_kind, ref.namespace, ref.name, ref.rev)
    : repoBase(ref.target_kind, ref.namespace, ref.name);
}

/** How a reference reads in the UI: "team/imdb-ja@v1", or the raw text. */
export function lineageRefLabel(ref: LineageRef): string {
  if (!ref.full_name) return ref.raw;
  if (ref.kind === "run") return `${ref.full_name}/${ref.project}/${ref.run}`;
  return ref.rev ? `${ref.full_name}@${ref.rev}` : ref.full_name;
}
