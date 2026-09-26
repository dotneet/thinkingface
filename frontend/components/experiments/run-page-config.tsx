"use client";

import { useMemo, useState } from "react";

import { ConfigEntryTable } from "@/components/experiments/config-entry-table";
import { Checkbox } from "@/components/ui/field";
import { FilterInput } from "@/components/ui/search-input";
import { SegmentedControl } from "@/components/ui/segmented-control";
import {
  CONFIG_FILTER_THRESHOLD,
  configKeysDifferingFrom,
  entriesWithBaselineKeys,
  filterConfigEntries,
} from "@/lib/exp-runpage-config";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { type ConfigEntry, splitRunConfig } from "@/lib/run-config";
import type { ExpRun } from "@/types/api";

type ConfigTab = "params" | "args";

/**
 * The run's config, where the reader looks right after the numbers: the
 * hyperparameters (and, when the HF Trainer logged them, its
 * TrainingArguments behind a switch), filterable once the list is long.
 *
 * "Diff vs baseline" adds the project baseline's value next to each key and
 * marks the keys that differ — the usual question being "what did this run
 * change?". The environment snapshot (`_meta`) is not here; it lives with the
 * run's outputs further down.
 */
export function RunPageConfig({ run, baseline }: { run: ExpRun; baseline: ExpRun | undefined }) {
  const t = useT();
  const config = useMemo(() => splitRunConfig(run.config), [run]);
  const baseConfig = useMemo(
    () => (baseline ? splitRunConfig(baseline.config) : undefined),
    [baseline],
  );
  const [tab, setTab] = useState<ConfigTab>("params");
  const [query, setQuery] = useState("");
  const [diff, setDiff] = useState(false);

  const activeTab: ConfigTab = tab === "args" && config.args.length > 0 ? "args" : "params";
  const own: ConfigEntry[] = activeTab === "args" ? config.args : config.params;
  const base: ConfigEntry[] | undefined =
    baseConfig && (activeTab === "args" ? baseConfig.args : baseConfig.params);
  const comparing = diff && base !== undefined;

  const differing = useMemo(
    () => (base ? configKeysDifferingFrom(own, base) : new Set<string>()),
    [own, base],
  );
  const rows = useMemo(() => {
    const all = comparing && base ? entriesWithBaselineKeys(own, base) : own;
    return filterConfigEntries(all, query);
  }, [own, base, comparing, query]);
  const baseValues = useMemo(
    () => new Map((base ?? []).map((entry) => [entry.key, entry.value])),
    [base],
  );

  const showFilter =
    config.params.length > CONFIG_FILTER_THRESHOLD || config.args.length > CONFIG_FILTER_THRESHOLD;

  return (
    <section className="flex min-w-0 flex-col gap-2">
      <div className="flex min-h-8 flex-wrap items-center gap-x-3 gap-y-2">
        <h2 className="text-sm font-semibold">{t("experiments.runPage.configTitle")}</h2>
        {config.args.length > 0 && (
          <SegmentedControl<ConfigTab>
            value={activeTab}
            onChange={setTab}
            label={t("experiments.runPage.configSectionsAria")}
            options={[
              {
                value: "params",
                label: `${t("experiments.runPage.configTabParams")} ${formatNumber(config.params.length)}`,
              },
              {
                value: "args",
                label: `${t("experiments.runPage.configTabArgs")} ${formatNumber(config.args.length)}`,
              },
            ]}
          />
        )}
        {baseline && (
          <label
            className="ml-auto flex items-center gap-1.5 text-xs font-medium text-fg-muted"
            title={t("experiments.runPage.diffToggleHint", { baseline: baseline.name })}
          >
            <Checkbox checked={diff} onChange={(e) => setDiff(e.target.checked)} />
            {t("experiments.runPage.diffToggle")}
          </label>
        )}
      </div>

      {(showFilter || comparing) && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          {showFilter && (
            <FilterInput
              value={query}
              onChange={setQuery}
              placeholder={t("experiments.runPage.configFilter")}
              wrapperClassName="w-full max-w-xs"
            />
          )}
          {comparing && (
            <span className="text-xs font-medium text-fg-subtle tabular-nums">
              {differing.size === 0
                ? t("experiments.runPage.diffNone")
                : t(
                    differing.size === 1
                      ? "experiments.runPage.diffCountOne"
                      : "experiments.runPage.diffCountOther",
                    { count: formatNumber(differing.size) },
                  )}
            </span>
          )}
        </div>
      )}

      {own.length > 0 && rows.length === 0 ? (
        <p className="rounded-lg border border-border px-3 py-4 text-center text-sm text-fg-subtle">
          {t("experiments.runPage.configNoMatch", { query })}
        </p>
      ) : (
        <ConfigEntryTable
          entries={rows}
          emptyTitle={t("experiments.run.paramsEmptyTitle")}
          emptyDescription={t("experiments.run.paramsEmptyDescription")}
          baseline={
            comparing && baseline
              ? {
                  label: t("experiments.runPage.colBaseline", { name: baseline.name }),
                  values: baseValues,
                  differing,
                }
              : undefined
          }
        />
      )}
    </section>
  );
}
