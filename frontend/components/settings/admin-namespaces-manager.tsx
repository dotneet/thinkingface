"use client";

import { HardDrive, Pencil, RefreshCw } from "lucide-react";
import { useCallback, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Input } from "@/components/ui/field";
import { OutOfRangeEmptyState, PaginationControls } from "@/components/ui/pagination-controls";
import { SearchInput } from "@/components/ui/search-input";
import { SkeletonLines } from "@/components/ui/skeleton";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
import { usePagedList } from "@/hooks/use-paged-list";
import type { AdminNamespaceUsage } from "@/lib/admin";
import { listAdminNamespaces, namespaceQuotaErrorKey, setNamespaceQuota } from "@/lib/admin";
import type { FailedApiResult } from "@/lib/api-error-message";
import { errorMessage } from "@/lib/api-error-message";
import { formatBytes, formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

/** The store's own default page size, restated so the two agree. */
const PAGE_SIZE = 50;

/**
 * Storage quotas, per namespace (docs/dev/api-contract.md §1.3).
 *
 * The screen exists because object storage is billed by the byte and nothing
 * else on the instance caps it: without a quota one namespace can fill the
 * bucket, and the usage dashboard only reports that afterwards.
 *
 * The distinction the whole UI has to preserve is between a namespace with no
 * quota of its own — which is held to the instance default and shown as
 * "Instance default" — and one deliberately set to 0, which refuses every
 * upload. An empty input clears the override; a typed `0` sets one. They are
 * different requests on the wire (`null` vs `0`) and are kept different here.
 *
 * Quotas are enforced on the LFS upload path, so nothing on this screen ever
 * deletes bytes: lowering a quota below what a namespace already holds shows
 * it as over quota and refuses its next upload.
 */
export function AdminNamespacesManager() {
  const t = useT();
  const [search, setSearch] = useState("");

  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [reloading, setReloading] = useState(false);

  const describe = useCallback(
    (result: FailedApiResult) => {
      const key = namespaceQuotaErrorKey(result);
      return key ? t(key) : errorMessage(t, result);
    },
    [t],
  );

  const {
    items: rows,
    data,
    total,
    setOffset,
    loadError,
    reload: refresh,
    outOfRange,
    pager,
  } = usePagedList({
    pageSize: PAGE_SIZE,
    deps: [search],
    fetchPage: ({ limit, offset }) => listAdminNamespaces({ search, limit, offset }),
    describe,
  });

  // `undefined` is "we have not had a successful read", which is not the same
  // as the instance having no default quota (DESIGN.md §9), so the two are
  // kept apart rather than collapsed into one nullable number.
  const defaultQuota: number | null | undefined = data?.default_quota_bytes;

  function runSearch(value: string) {
    setSearch(value);
    setOffset(0);
  }

  async function handleReload() {
    setReloading(true);
    setActionError(null);
    setNotice(null);
    await refresh();
    setReloading(false);
  }

  function startEdit(row: AdminNamespaceUsage) {
    setEditing(row.namespace);
    // The field is seeded with the namespace's own override only. Seeding it
    // with the effective ceiling would turn "cancel" into "adopt the instance
    // default as an override", which is a change the administrator did not ask
    // for.
    setDraft(row.quota_bytes === null ? "" : String(row.quota_bytes));
    setActionError(null);
    setNotice(null);
  }

  async function handleSave(row: AdminNamespaceUsage) {
    const raw = draft.trim();
    // An empty field is "clear the override" (null on the wire); anything else
    // has to be a whole, non-negative byte count. `Number` accepts "1e3" and
    // "0x10", so the parse is deliberately strict rather than lenient.
    let quota: number | null = null;
    if (raw !== "") {
      if (!/^\d+$/.test(raw) || !Number.isSafeInteger(Number(raw))) {
        setActionError(t("settings.adminQuotas.quotaInvalid"));
        return;
      }
      quota = Number(raw);
    }

    setBusy(row.namespace);
    setActionError(null);
    setNotice(null);
    const result = await setNamespaceQuota(row.namespace, quota);
    setBusy(null);
    if (!result.ok) {
      setActionError(describe(result));
      // A 404 means the namespace is gone, so the listing on screen is the
      // stale part — re-read it rather than leaving a row that no longer
      // exists next to the error.
      if (result.status === 404) await refresh();
      return;
    }
    setEditing(null);
    setNotice(
      quota === null
        ? t("settings.adminQuotas.cleared", { namespace: row.namespace })
        : t("settings.adminQuotas.saved", {
            namespace: row.namespace,
            quota: formatBytes(quota),
          }),
    );
    // Re-read rather than patching the row in place: usage moves with every
    // push, and the row's neighbours may have changed while this was open.
    await refresh();
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <SearchInput
          activeValue={search}
          onSearch={runSearch}
          placeholder={t("settings.adminQuotas.searchPlaceholder")}
          formClassName="min-w-[240px] flex-1"
        />
        {/* Only ever rendered from a successful read (DESIGN.md §9). */}
        {total !== null && (
          <span className="text-xs font-medium tabular-nums text-fg-subtle">
            {t(total === 1 ? "settings.adminQuotas.countOne" : "settings.adminQuotas.countOther", {
              count: formatNumber(total),
            })}
          </span>
        )}
        <Button size="sm" onClick={handleReload} disabled={reloading || busy !== null}>
          <RefreshCw size={14} />
          {t("settings.adminQuotas.refresh")}
        </Button>
      </div>

      {defaultQuota !== undefined && (
        <p className="text-xs font-medium text-fg-subtle">
          {defaultQuota === null
            ? t("settings.adminQuotas.defaultQuotaUnlimited")
            : t("settings.adminQuotas.defaultQuota", { quota: formatBytes(defaultQuota) })}{" "}
          {t("settings.adminQuotas.defaultQuotaNote")}
        </p>
      )}

      {rows === null && !loadError ? (
        <SkeletonLines lines={5} />
      ) : rows === null ? (
        <ErrorState
          title={t("settings.adminQuotas.loadFailed")}
          message={loadError ?? t("settings.adminQuotas.loadFailed")}
          hint={t("settings.adminQuotas.loadFailedHint")}
          action={
            <Button size="sm" onClick={() => handleReload()}>
              {t("ui.unexpectedError.retry")}
            </Button>
          }
        />
      ) : rows.length === 0 ? (
        outOfRange ? (
          <OutOfRangeEmptyState onBackToFirstPage={() => setOffset(0)} />
        ) : (
          <EmptyState
            icon={HardDrive}
            title={t("settings.adminQuotas.emptyTitle")}
            description={t("settings.adminQuotas.emptyDescription")}
          />
        )
      ) : (
        <Table minWidth={760}>
          <THead>
            <Th>{t("settings.adminQuotas.colNamespace")}</Th>
            <Th>{t("settings.adminQuotas.colKind")}</Th>
            <Th align="right">{t("settings.adminQuotas.colRepos")}</Th>
            <Th align="right">{t("settings.adminQuotas.colUsed")}</Th>
            <Th>{t("settings.adminQuotas.colQuota")}</Th>
            <Th align="right">{t("settings.adminQuotas.colActions")}</Th>
          </THead>
          <TBody>
            {rows.map((row) => (
              <Tr key={row.namespace} className="align-top">
                <Td className="font-medium text-fg">{row.namespace}</Td>
                <Td className="text-fg-muted">
                  {row.kind === "org"
                    ? t("settings.adminQuotas.kindOrg")
                    : t("settings.adminQuotas.kindUser")}
                </Td>
                <Td align="right" className="tabular-nums text-fg-muted">
                  {formatNumber(row.num_repos)}
                </Td>
                <Td align="right" className="tabular-nums text-fg-muted">
                  {formatBytes(row.lfs_size)}
                </Td>
                <Td>
                  {editing === row.namespace ? (
                    <QuotaEditor
                      value={draft}
                      onChange={setDraft}
                      busy={busy === row.namespace}
                      onSave={() => void handleSave(row)}
                      onCancel={() => {
                        setEditing(null);
                        // Cancel closes the editor but used to leave an
                        // invalid-input actionError (from handleSave's byte-count
                        // check) sitting under the table with nothing left on
                        // screen for it to be about.
                        setActionError(null);
                      }}
                    />
                  ) : (
                    <QuotaCell row={row} />
                  )}
                </Td>
                <Td>
                  <div className="flex justify-end">
                    <Button
                      size="sm"
                      // Disabled for *every* row, not just this one, while any
                      // row is being edited: `startEdit` unconditionally
                      // overwrites `draft`, so clicking another row's Edit used
                      // to discard whatever was typed here with no warning at
                      // all.
                      disabled={busy !== null || editing !== null}
                      onClick={() => startEdit(row)}
                    >
                      <Pencil size={13} />
                      {t("settings.adminQuotas.edit")}
                    </Button>
                  </div>
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}

      <PaginationControls pager={pager} />

      {/* Below the table and its pager: a quota save reported above them moved
          every row down by the Alert's height, with the Edit control of the
          next namespace landing where the pointer already was
          (DESIGN.md §8.1). */}
      {actionError && <Alert tone="negative">{actionError}</Alert>}
      {notice && <Alert tone="positive">{notice}</Alert>}
    </div>
  );
}

/**
 * What a namespace is actually held to, and where that number came from. The
 * provenance matters: "1 GB, because the instance says so" and "1 GB, because
 * somebody set it here" respond differently to the default being changed.
 */
function QuotaCell({ row }: { row: AdminNamespaceUsage }) {
  const t = useT();
  const effective = row.effective_quota_bytes;
  const over = effective !== null && row.lfs_size > effective;

  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="tabular-nums text-fg">
        {effective === null ? t("settings.adminQuotas.unlimited") : formatBytes(effective)}
      </span>
      {row.quota_bytes === null && (
        <Badge tone="muted">{t("settings.adminQuotas.inherited")}</Badge>
      )}
      {over && <Badge tone="negative">{t("settings.adminQuotas.overQuota")}</Badge>}
    </div>
  );
}

/**
 * The inline editor. It is a plain byte count rather than a "10 GB" parser:
 * the wire value is bytes, and a unit box that silently rounds is a worse
 * trade than typing the digits. The parsed value is echoed back formatted
 * underneath, so a mistyped order of magnitude is visible before saving.
 */
function QuotaEditor({
  value,
  onChange,
  busy,
  onSave,
  onCancel,
}: {
  value: string;
  onChange: (v: string) => void;
  busy: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const t = useT();
  const trimmed = value.trim();
  const parsed = /^\d+$/.test(trimmed) ? Number(trimmed) : null;

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          // A single-row control with no visible <label> of its own; the
          // column header names the column, not this field.
          aria-label={t("settings.adminQuotas.quotaLabel")}
          inputMode="numeric"
          value={value}
          disabled={busy}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              onSave();
            }
            if (e.key === "Escape") onCancel();
          }}
          className="w-40 px-2 py-1 text-sm"
        />
        <Button size="sm" variant="primary" disabled={busy} onClick={onSave}>
          {busy ? t("settings.adminQuotas.saving") : t("settings.adminQuotas.save")}
        </Button>
        <Button size="sm" disabled={busy} onClick={onCancel}>
          {t("settings.adminQuotas.cancel")}
        </Button>
      </div>
      <span className="max-w-[22rem] text-xs font-medium text-fg-subtle">
        {parsed === null
          ? t("settings.adminQuotas.quotaHint")
          : `${formatBytes(parsed)} — ${t("settings.adminQuotas.quotaHint")}`}
      </span>
    </div>
  );
}
