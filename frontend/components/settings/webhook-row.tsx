"use client";

import { ChevronDown, ChevronRight, KeyRound, Trash2 } from "lucide-react";
import { useState } from "react";
import { WebhookDeliveriesPanel } from "@/components/settings/webhook-deliveries-panel";
import { WEBHOOK_EVENT_OPTIONS } from "@/components/settings/webhook-events";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Checkbox, Field, Input } from "@/components/ui/field";
import { useFormattedTime } from "@/components/ui/time-text";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { deleteWebhook, updateWebhook } from "@/lib/webhooks";
import type { Webhook, WebhookEvent } from "@/types/api";

export function WebhookRow({
  webhook,
  onChanged,
  onDeleted,
}: {
  webhook: Webhook;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const t = useT();
  const createdAt = useFormattedTime(webhook.created_at);
  const [editing, setEditing] = useState(false);
  const [showDeliveries, setShowDeliveries] = useState(false);
  const [url, setUrl] = useState(webhook.url);
  const [events, setEvents] = useState<Set<WebhookEvent>>(new Set(webhook.events));
  const [active, setActive] = useState(webhook.active);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [rotatedSecret, setRotatedSecret] = useState<string | null>(null);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [confirmRotateOpen, setConfirmRotateOpen] = useState(false);

  function toggleEvent(e: WebhookEvent) {
    setEvents((prev) => {
      const next = new Set(prev);
      if (next.has(e)) next.delete(e);
      else next.add(e);
      return next;
    });
  }

  // `url` / `events` / `active` are local edit buffers, not a mirror of the
  // `webhook` prop: React only re-initializes `useState(webhook.active)` on
  // mount, and `WebhooksManager` re-renders this row with the same `key`
  // after every refetch, so the row never remounts. Without this reset,
  // toggling Enable/Disable (which only updates the server and the parent's
  // list, never these local buffers) leaves `active` permanently stale, and
  // the next unrelated Save silently carries the old value back to the
  // server — undoing the toggle. Re-seeding the buffers from the current
  // prop every time the panel opens keeps them honest instead.
  function toggleEditing() {
    if (!editing) {
      setUrl(webhook.url);
      setEvents(new Set(webhook.events));
      setActive(webhook.active);
      setError(null);
      setRotatedSecret(null);
    }
    setEditing((v) => !v);
  }

  async function handleSave(rotateSecret: boolean) {
    if (events.size === 0) {
      setError(t("settings.webhooks.selectAtLeastOneEvent"));
      return;
    }
    setSaving(true);
    setError(null);
    const result = await updateWebhook(webhook.id, {
      url,
      events: Array.from(events),
      active,
      rotate_secret: rotateSecret,
    });
    setSaving(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    if (result.data.secret) setRotatedSecret(result.data.secret);
    setConfirmRotateOpen(false);
    setEditing(rotateSecret); // stay open to show the rotated secret
    onChanged();
  }

  async function handleToggleActive() {
    setSaving(true);
    setError(null);
    const result = await updateWebhook(webhook.id, { active: !webhook.active });
    setSaving(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    onChanged();
  }

  async function handleDelete() {
    setDeleting(true);
    setDeleteError(null);
    const result = await deleteWebhook(webhook.id);
    setDeleting(false);
    if (!result.ok) {
      setDeleteError(errorMessage(t, result));
      return;
    }
    setConfirmDeleteOpen(false);
    onDeleted();
  }

  return (
    <div className="rounded-lg border border-border">
      <div className="flex flex-wrap items-center gap-3 p-3">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setShowDeliveries((v) => !v)}
          className="px-1 text-fg-subtle hover:text-fg"
          aria-label={
            showDeliveries
              ? t("settings.webhooks.hideDeliveries")
              : t("settings.webhooks.showDeliveries")
          }
        >
          {showDeliveries ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </Button>
        <div className="min-w-0 flex-1">
          <div className="scroll-x whitespace-pre font-mono text-sm">{webhook.url}</div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <Badge tone={webhook.repo_full_name ? "accent" : "neutral"}>
              {webhook.repo_full_name ||
                t("settings.webhooks.allRepos", { namespace: webhook.namespace })}
            </Badge>
            {webhook.events.map((e) => (
              <Badge key={e}>{e}</Badge>
            ))}
            {!webhook.active && (
              <Badge tone="warning">{t("settings.webhooks.disabledBadge")}</Badge>
            )}
          </div>
          <div className="mt-1 text-xs font-medium text-fg-subtle">
            {t("settings.webhooks.createdAt", { date: createdAt })}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button size="sm" disabled={saving} onClick={handleToggleActive}>
            {webhook.active ? t("settings.webhooks.disable") : t("settings.webhooks.enable")}
          </Button>
          <Button size="sm" onClick={toggleEditing}>
            {editing ? t("settings.webhooks.close") : t("settings.webhooks.edit")}
          </Button>
          <Button
            variant="danger"
            size="sm"
            disabled={deleting}
            onClick={() => {
              setDeleteError(null);
              setConfirmDeleteOpen(true);
            }}
          >
            <Trash2 size={12} />
            {deleting ? t("settings.webhooks.deleting") : t("settings.webhooks.delete")}
          </Button>
        </div>
      </div>

      {/* Only rendered here while the editing panel is closed: while it's
          open the same error renders inside the panel, below its Save/Rotate
          row, so it can never push those buttons down (DESIGN.md §8). */}
      {error && !editing && (
        <div className="px-3 pb-3">
          <Alert tone="negative">{error}</Alert>
        </div>
      )}

      {editing && (
        <div className="flex flex-col gap-3 border-t border-border bg-bg-sunken p-3">
          {rotatedSecret && (
            <Alert tone="positive" title={t("settings.webhooks.secretRotatedTitle")}>
              <p className="text-xs font-medium text-fg-subtle">
                {t("settings.webhooks.secretRotatedCopy")}
              </p>
              <code className="mt-1.5 block scroll-x whitespace-pre rounded-md border border-border bg-bg-raised p-2 font-mono text-xs">
                {rotatedSecret}
              </code>
            </Alert>
          )}
          <Field label={t("settings.webhooks.urlLabel")}>
            <Input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://example.com/hook"
            />
          </Field>
          <div className="flex flex-col gap-1.5">
            <span className="text-sm font-medium text-fg-muted">
              {t("settings.webhooks.eventsLabel")}
            </span>
            <div className="flex flex-col gap-1.5">
              {WEBHOOK_EVENT_OPTIONS.map((opt) => (
                <label key={opt.value} className="flex items-center gap-2 text-sm text-fg-muted">
                  <Checkbox
                    checked={events.has(opt.value)}
                    onChange={() => toggleEvent(opt.value)}
                  />
                  {t(opt.labelKey)}
                  <span className="text-xs font-medium text-fg-subtle">— {t(opt.hintKey)}</span>
                </label>
              ))}
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm text-fg-muted">
            <Checkbox checked={active} onChange={(e) => setActive(e.target.checked)} />
            {t("settings.webhooks.active")}
          </label>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" size="sm" disabled={saving} onClick={() => handleSave(false)}>
              {saving ? t("settings.webhooks.saving") : t("settings.webhooks.save")}
            </Button>
            <Button
              size="sm"
              disabled={saving}
              onClick={() => {
                setError(null);
                setConfirmRotateOpen(true);
              }}
            >
              <KeyRound size={12} />
              {t("settings.webhooks.rotateSecret")}
            </Button>
          </div>
          {error && <Alert tone="negative">{error}</Alert>}
        </div>
      )}

      {showDeliveries && (
        <div className="border-t border-border p-3">
          <WebhookDeliveriesPanel webhookId={webhook.id} />
        </div>
      )}

      <ConfirmDialog
        open={confirmDeleteOpen}
        onClose={() => setConfirmDeleteOpen(false)}
        onConfirm={handleDelete}
        title={t("settings.webhooks.confirmDeleteTitle")}
        description={
          <p className="text-sm text-fg-muted">
            {t("settings.webhooks.confirmDelete", { url: webhook.url })}
          </p>
        }
        confirmLabel={t("settings.webhooks.delete")}
        confirmingLabel={t("settings.webhooks.deleting")}
        confirming={deleting}
        error={deleteError}
      />

      <ConfirmDialog
        open={confirmRotateOpen}
        onClose={() => setConfirmRotateOpen(false)}
        onConfirm={() => handleSave(true)}
        title={t("settings.webhooks.confirmRotateTitle")}
        description={
          <p className="text-sm text-fg-muted">{t("settings.webhooks.confirmRotate")}</p>
        }
        confirmLabel={t("settings.webhooks.rotateSecret")}
        confirmingLabel={t("settings.webhooks.saving")}
        confirming={saving}
        error={error}
      />
    </div>
  );
}
