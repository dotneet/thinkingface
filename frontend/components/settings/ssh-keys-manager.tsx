"use client";

import { ChevronDown, ChevronRight, KeyRound, Trash2 } from "lucide-react";
import { Fragment, useCallback, useEffect, useState } from "react";
import { LoginRequiredState } from "@/components/settings/login-required-state";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Textarea } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
import { TimeText } from "@/components/ui/time-text";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { createSSHKey, deleteSSHKey, listSSHKeys } from "@/lib/ssh-keys";
import type { SSHKeyItem } from "@/types/api";

export function SSHKeysManager() {
  const t = useT();
  const [keys, setKeys] = useState<SSHKeyItem[] | null>(null);
  // Split from the add-form's own error (see handleAdd): they used to share
  // one `error` state, so a list-load failure and an add failure could each
  // stomp on the other's message, and the ErrorState meant for "the list
  // didn't load" could end up showing the add form's error text instead.
  const [loadError, setLoadError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  const [newKey, setNewKey] = useState("");
  const [adding, setAdding] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);
  const [justAdded, setJustAdded] = useState<SSHKeyItem | null>(null);
  // Id of the row whose delete is in flight, so a second click on the same
  // button can't fire a second DELETE (the first succeeds, the second 404s and
  // surfaces a confusing "not found" for a key the user did delete).
  const [deletingId, setDeletingId] = useState<number | null>(null);
  // Id of the row pending confirmation in the ConfirmDialog; null closes it.
  const [confirmDeleteId, setConfirmDeleteId] = useState<number | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  // Id of the row whose stored public key is expanded. A public key is not a
  // secret, but it is one very long line, so it stays collapsed until asked
  // for. One at a time: the point is to read *this* key, and keeping the list
  // short keeps the toggles near where the pointer already is.
  const [expandedKeyId, setExpandedKeyId] = useState<number | null>(null);

  // Stable across renders (it only closes over `t`) so the initial-load
  // effect below can depend on it: re-running on a locale change also
  // re-renders a stale load error in the new language.
  const refresh = useCallback(async () => {
    const result = await listSSHKeys();
    if (!result.ok) {
      // 401 here means "you're signed out", not "something broke" — say so
      // and point at the fix instead of surfacing the raw API error.
      setNeedsLogin(isUnauthorized(result));
      setLoadError(errorMessage(t, result));
      setKeys(null);
      return;
    }
    setNeedsLogin(false);
    setLoadError(null);
    setKeys(result.data.items);
  }, [t]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    if (!newKey.trim()) return;
    setAdding(true);
    setAddError(null);
    // Drop the previous key's "added" banner before asking for a new one: a
    // failed add would otherwise leave key A's banner standing where it
    // reads as the result of the attempt that just failed for key B.
    setJustAdded(null);
    const result = await createSSHKey(newTitle.trim(), newKey.trim());
    setAdding(false);
    if (!result.ok) {
      // Anything but 401 is the backend's own validation message ("RSA keys
      // must be at least 2048 bits", "this key is already registered"), which
      // is written for the person pasting the key — show it verbatim
      // (bad_request keeps its detail, see lib/api-error-message.ts).
      setAddError(
        isUnauthorized(result)
          ? t("settings.sshKeys.sessionExpiredCreate")
          : errorMessage(t, result),
      );
      return;
    }
    setJustAdded(result.data);
    setNewTitle("");
    setNewKey("");
    await refresh();
  }

  async function handleDelete() {
    if (confirmDeleteId === null || deletingId !== null) return;
    const id = confirmDeleteId;
    setDeleteError(null);
    setDeletingId(id);
    const result = await deleteSSHKey(id);
    setDeletingId(null);
    if (!result.ok) {
      setDeleteError(
        isUnauthorized(result)
          ? t("settings.sshKeys.sessionExpiredDelete")
          : errorMessage(t, result),
      );
      return;
    }
    setConfirmDeleteId(null);
    await refresh();
  }

  // The row the confirmation dialog is about, so it can name the key instead
  // of asking about "this SSH key" on a screen listing several — "work
  // laptop", "home desktop" and "ci runner" otherwise produce a byte-identical
  // dialog.
  const confirmDeleteKey = keys?.find((key) => key.id === confirmDeleteId);

  return (
    <div className="flex flex-col gap-6">
      {!needsLogin && (
        <form
          onSubmit={handleAdd}
          className="flex flex-col gap-3 rounded-lg border border-border bg-bg-sunken p-4"
        >
          <Field label={t("settings.sshKeys.titleLabel")}>
            <Input
              value={newTitle}
              onChange={(e) => setNewTitle(e.target.value)}
              placeholder={t("settings.sshKeys.titlePlaceholder")}
            />
          </Field>
          <Field label={t("settings.sshKeys.keyLabel")} hint={t("settings.sshKeys.keyHint")}>
            <Textarea
              value={newKey}
              onChange={(e) => setNewKey(e.target.value)}
              placeholder={t("settings.sshKeys.keyPlaceholder")}
              rows={4}
              spellCheck={false}
              className="font-mono text-xs"
            />
          </Field>
          <div>
            <Button
              type="submit"
              variant="primary"
              disabled={adding || !newKey.trim()}
              className="px-4 py-2"
            >
              {adding ? t("settings.sshKeys.adding") : t("settings.sshKeys.add")}
            </Button>
          </div>
          {/* Below the submit button, and its own state from the list-load
              error below (DESIGN.md §8): a failed add must never be mistaken
              for the list having failed to load, or vice versa. */}
          {addError && <Alert tone="negative">{addError}</Alert>}
        </form>
      )}

      {justAdded && (
        <Alert tone="positive" title={t("settings.sshKeys.addedTitle", { title: justAdded.title })}>
          <p className="text-xs font-medium text-fg-subtle">{t("settings.sshKeys.addedBody")}</p>
          <code className="mt-1.5 block scroll-x whitespace-pre font-mono text-xs">
            {justAdded.fingerprint}
          </code>
        </Alert>
      )}

      {keys === null && !loadError ? (
        <SkeletonLines lines={3} />
      ) : keys === null && needsLogin ? (
        <LoginRequiredState next="/settings/ssh-keys" />
      ) : keys === null ? (
        <ErrorState
          title={t("settings.errorTitle")}
          message={loadError ?? t("settings.sshKeys.loadFailed")}
          hint={t("settings.sshKeys.loadFailedHint")}
          action={
            <Button size="sm" onClick={() => refresh()}>
              {t("ui.unexpectedError.retry")}
            </Button>
          }
        />
      ) : keys.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title={t("settings.sshKeys.emptyTitle")}
          description={t("settings.sshKeys.emptyDescription")}
        />
      ) : (
        <Table minWidth={820}>
          <THead>
            <Th>{t("settings.sshKeys.colTitle")}</Th>
            <Th>{t("settings.sshKeys.colType")}</Th>
            <Th>{t("settings.sshKeys.colFingerprint")}</Th>
            <Th>{t("settings.sshKeys.colAdded")}</Th>
            <Th>{t("settings.sshKeys.colLastUsed")}</Th>
            <Th />
          </THead>
          <TBody>
            {keys.map((key) => {
              const expanded = expandedKeyId === key.id;
              const panelId = `ssh-key-${key.id}-public-key`;
              return (
                <Fragment key={key.id}>
                  {/* An expanded row runs straight into its panel below, so
                        it drops the divider the shell would otherwise draw. */}
                  <Tr className={expanded ? "border-b-0" : undefined}>
                    <Td className="font-medium">{key.title}</Td>
                    <Td className="font-mono text-xs text-fg-muted">{key.key_type}</Td>
                    <Td className="font-mono text-xs text-fg-muted">{key.fingerprint}</Td>
                    <Td className="text-fg-subtle">
                      <TimeText iso={key.created_at} style="dateTime" />
                    </Td>
                    <Td className="text-fg-subtle">
                      {key.last_used_at ? (
                        <TimeText iso={key.last_used_at} style="dateTime" />
                      ) : (
                        t("settings.sshKeys.neverUsed")
                      )}
                    </Td>
                    <Td>
                      <div className="flex items-center justify-end gap-2">
                        {/* The label never changes and the two chevrons are
                              the same size, so toggling cannot resize this
                              button and nudge the destructive Delete next to
                              it out from under the pointer (DESIGN.md §8).
                              The panel opens *below* this row, so the toggle
                              itself also stays put. */}
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-expanded={expanded}
                          aria-controls={panelId}
                          aria-label={t("settingsDetail.sshKeys.publicKeyToggleAria", {
                            title: key.title,
                          })}
                          onClick={() => setExpandedKeyId(expanded ? null : key.id)}
                        >
                          {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                          {t("settingsDetail.sshKeys.publicKeyToggle")}
                        </Button>
                        <Button
                          variant="danger"
                          size="sm"
                          disabled={deletingId !== null}
                          onClick={() => {
                            setDeleteError(null);
                            setConfirmDeleteId(key.id);
                          }}
                        >
                          <Trash2 size={12} />
                          {deletingId === key.id
                            ? t("settings.sshKeys.deleting")
                            : t("settings.sshKeys.delete")}
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                  {expanded && (
                    <Tr>
                      <Td id={panelId} colSpan={6} className="py-0 pb-3">
                        {/* CodeBlock's unlabelled layout: the key line is a
                              flex item with `overflow-x: auto`, so its
                              automatic minimum size collapses to 0 and the
                              line scrolls inside this cell instead of
                              stretching the table (and the page) sideways. */}
                        <CodeBlock
                          value={key.public_key}
                          copyLabel={t("settingsDetail.sshKeys.copyPublicKey")}
                        />
                      </Td>
                    </Tr>
                  )}
                </Fragment>
              );
            })}
          </TBody>
        </Table>
      )}

      <ConfirmDialog
        open={confirmDeleteId !== null}
        onClose={() => setConfirmDeleteId(null)}
        onConfirm={handleDelete}
        title={t("settings.sshKeys.confirmDeleteTitle")}
        description={
          <p className="text-sm text-fg-muted">
            {confirmDeleteKey
              ? t("settings.sshKeys.confirmDeleteNamed", {
                  // A title is optional; the fingerprint is not, and it
                  // identifies the row just as well.
                  title: confirmDeleteKey.title || confirmDeleteKey.fingerprint,
                })
              : t("settings.sshKeys.confirmDelete")}
          </p>
        }
        confirmLabel={t("settings.sshKeys.delete")}
        confirmingLabel={t("settings.sshKeys.deleting")}
        confirming={deletingId !== null}
        error={deleteError}
      />
    </div>
  );
}
