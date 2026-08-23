/**
 * Pure helpers for reading one run's `config` on the run detail page.
 *
 * `config` carries three different kinds of thing side by side, and the page
 * shows them in three different places:
 *
 * - `_meta` — the environment snapshot the trackio shim collects (git state,
 *   cmdline, python/platform, GPU, a hash of the installed packages);
 * - `_args` — the HF `TrainingArguments` the autolog integration records;
 * - everything else — the hyperparameters the user logged themselves.
 *
 * Both reserved keys arrive nested on the ingest path (`config["_meta"]` is an
 * object) but flattened on the parquet path, where a configs file has one
 * column per leaf (`_meta.git.commit`). Both spellings are folded to the same
 * dotted key here so the UI never has to care which route the run came in on.
 */

/** Reserved config key holding the run's environment snapshot. */
export const META_KEY = "_meta";
/** Reserved config key holding the HF Trainer's TrainingArguments. */
export const ARGS_KEY = "_args";

/** One leaf of a config, keyed by its dotted path. */
export type ConfigEntry = { key: string; value: unknown };

export type SplitConfig = {
  /** The hyperparameters the user logged, unflattened. */
  params: ConfigEntry[];
  /** `_args.*`, flattened and with the prefix stripped. */
  args: ConfigEntry[];
  /** `_meta.*`, flattened and with the prefix stripped. */
  meta: ConfigEntry[];
};

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Appends `value` to `out` under the dotted path `key`, descending into plain
 * objects. Arrays are leaves: `cmdline` reads as one command line, not as a
 * numbered list of argv entries.
 */
function flattenInto(out: ConfigEntry[], key: string, value: unknown): void {
  if (isPlainObject(value) && Object.keys(value).length > 0) {
    for (const [child, childValue] of Object.entries(value)) {
      flattenInto(out, key ? `${key}.${child}` : child, childValue);
    }
    return;
  }
  out.push({ key, value });
}

/** True when `key` is `prefix` itself or a dotted path beneath it. */
function underPrefix(key: string, prefix: string): boolean {
  return key === prefix || key.startsWith(`${prefix}.`);
}

function addUnderPrefix(out: ConfigEntry[], key: string, prefix: string, value: unknown): void {
  if (key !== prefix) {
    flattenInto(out, key.slice(prefix.length + 1), value);
    return;
  }
  // The bare reserved key: its children are the section, so an empty object
  // contributes nothing. A non-object there (a JSON string in a parquet config
  // column, say) has no path of its own, so it keeps the reserved key as name
  // rather than being emitted under an empty one.
  if (!isPlainObject(value)) {
    out.push({ key: prefix, value });
    return;
  }
  for (const [child, childValue] of Object.entries(value)) flattenInto(out, child, childValue);
}

/**
 * True for a config key belonging to one of the reserved sections. The config
 * diff table folds these away by default: a sweep differs on the git commit
 * and half the TrainingArguments in every run, which buries the two
 * hyperparameters that actually moved.
 */
export function isReservedConfigKey(key: string): boolean {
  return underPrefix(key, META_KEY) || underPrefix(key, ARGS_KEY);
}

const byKey = (a: ConfigEntry, b: ConfigEntry) => a.key.localeCompare(b.key);

/** Splits a run's config into the three sections the detail page renders. */
export function splitRunConfig(config: Record<string, unknown> | null | undefined): SplitConfig {
  const params: ConfigEntry[] = [];
  const args: ConfigEntry[] = [];
  const meta: ConfigEntry[] = [];
  for (const [key, value] of Object.entries(config ?? {})) {
    if (underPrefix(key, META_KEY)) addUnderPrefix(meta, key, META_KEY, value);
    else if (underPrefix(key, ARGS_KEY)) addUnderPrefix(args, key, ARGS_KEY, value);
    else params.push({ key, value });
  }
  return {
    params: params.sort(byKey),
    args: args.sort(byKey),
    meta: meta.sort(byKey),
  };
}

/**
 * The environment snapshot, with the fields the UI gives a dedicated row
 * pulled out of the flattened `_meta` entries. Anything the shim adds later
 * that this does not know about still shows up, under `extra`.
 */
export type RunEnv = {
  gitCommit?: string;
  gitBranch?: string;
  gitDirty?: boolean;
  /** argv, already masked client-side by the shim. */
  cmdline?: string;
  python?: string;
  platform?: string;
  hostname?: string;
  gpuName?: string;
  gpuCount?: number;
  cuda?: string;
  requirementsSha256?: string;
  extra: ConfigEntry[];
};

function asString(value: unknown): string | undefined {
  if (typeof value === "string") return value || undefined;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return undefined;
}

/** Renders a `cmdline` value: an argv array joins with spaces. */
function asCmdline(value: unknown): string | undefined {
  if (Array.isArray(value)) {
    const parts = value.map((part) => asString(part) ?? "").filter((part) => part !== "");
    return parts.length > 0 ? parts.join(" ") : undefined;
  }
  return asString(value);
}

/** Reads the known environment fields out of the flattened `_meta` entries. */
export function runEnv(meta: ConfigEntry[]): RunEnv {
  const env: RunEnv = { extra: [] };
  for (const entry of meta) {
    switch (entry.key) {
      case "git.commit":
        env.gitCommit = asString(entry.value);
        break;
      case "git.branch":
        env.gitBranch = asString(entry.value);
        break;
      case "git.dirty":
        env.gitDirty = Boolean(entry.value);
        break;
      case "cmdline":
        env.cmdline = asCmdline(entry.value);
        break;
      case "python":
        env.python = asString(entry.value);
        break;
      case "platform":
        env.platform = asString(entry.value);
        break;
      case "hostname":
        env.hostname = asString(entry.value);
        break;
      case "gpu.name":
        env.gpuName = asString(entry.value);
        break;
      case "gpu.count":
        env.gpuCount = typeof entry.value === "number" ? entry.value : Number(entry.value);
        break;
      case "gpu.cuda":
      case "cuda":
        env.cuda = asString(entry.value);
        break;
      case "requirements_sha256":
        env.requirementsSha256 = asString(entry.value);
        break;
      default:
        env.extra.push(entry);
    }
  }
  if (env.gpuCount !== undefined && !Number.isFinite(env.gpuCount)) env.gpuCount = undefined;
  return env;
}

/** True when the snapshot has nothing worth rendering. */
export function isEnvEmpty(env: RunEnv): boolean {
  return (
    env.extra.length === 0 &&
    env.gitCommit === undefined &&
    env.gitBranch === undefined &&
    env.gitDirty === undefined &&
    env.cmdline === undefined &&
    env.python === undefined &&
    env.platform === undefined &&
    env.hostname === undefined &&
    env.gpuName === undefined &&
    env.gpuCount === undefined &&
    env.cuda === undefined &&
    env.requirementsSha256 === undefined
  );
}
