"use client";

import { Webhook as WebhookIcon } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { WEBHOOK_EVENT_OPTIONS } from "@/components/settings/webhook-events";
import { WebhookRow } from "@/components/settings/webhook-row";
import { Alert } from "@/components/ui/alert";
import { Button, buttonClass } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Checkbox, Field, Input, Select } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getMe } from "@/lib/auth";
import { useT } from "@/lib/i18n/client";
import { listRepos } from "@/lib/repos";
import { createWebhook, listWebhooks } from "@/lib/webhooks";
import type { RepoSummary, User, Webhook, WebhookEvent } from "@/types/api";

// Only namespace admins may see or manage webhooks — mirrors the admin-only
// bar the backend enforces on every webhook endpoint (requireNamespaceAdmin
// in backend/internal/api/webhooks.go). A webhook carries the namespace's
// secrets to an external URL, which the backend treats as an administrative
// act; "write" members get a 403 from the API, so they must not see the
// namespace as an option here either.
function adminNamespaces(user: User): string[] {
  return user.namespaces.filter((n) => n.role === "admin").map((n) => n.name);
}

export function WebhooksManager({
  /**
   * Pins the manager to one namespace and hides the picker — how an
   * organisation's own settings screen embeds it (docs/dev/organization-design.md
   * §8.1). Left out on /settings/webhooks, where the user picks from every
   * namespace they can write to.
   */
  namespace: fixedNamespace,
  /** Where to send an unauthenticated visitor back to after logging in. */
  loginNext = "/settings/webhooks",
}: {
  namespace?: string;
  loginNext?: string;
} = {}) {
  const t = useT();
  const [user, setUser] = useState<User | null | undefined>(undefined); // undefined = loading
  const [needsLogin, setNeedsLogin] = useState(false);
  const [namespace, setNamespace] = useState(fixedNamespace ?? "");
  const [repos, setRepos] = useState<RepoSummary[] | null>(null); // null = loading or failed
  const [reposError, setReposError] = useState(false);
  const [webhooks, setWebhooks] = useState<Webhook[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [repoScope, setRepoScope] = useState(""); // "" | "kind/name"
  const [url, setUrl] = useState("");
  const [events, setEvents] = useState<Set<WebhookEvent>>(new Set());
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [justCreated, setJustCreated] = useState<Webhook & { secret: string }>();

  useEffect(() => {
    (async () => {
      const result = await getMe();
      if (!result.ok) {
        setNeedsLogin(isUnauthorized(result));
        setUser(null);
        return;
      }
      setUser(result.data.user);
      if (fixedNamespace) return;
      const namespaces = adminNamespaces(result.data.user);
      const first = namespaces[0];
      if (first) setNamespace(first);
    })();
  }, [fixedNamespace]);

  // `isStale` lets a caller opt out of a response that arrived after the
  // namespace (or the whole component) has moved on — see the effect below,
  // which is the only caller that can race. The direct calls from
  // `onChanged`/`onDeleted`/`handleCreate` are single, user-triggered
  // refreshes with nothing else in flight, so they use the default no-op.
  async function refreshWebhooks(ns: string, isStale: () => boolean = () => false) {
    const result = await listWebhooks(ns);
    if (isStale()) return;
    if (!result.ok) {
      setError(errorMessage(t, result));
      setWebhooks(null);
      return;
    }
    setError(null);
    setWebhooks(result.data.items);
  }

  useEffect(() => {
    if (!namespace) return;
    let cancelled = false;
    setWebhooks(null);
    setRepos(null);
    setReposError(false);
    refreshWebhooks(namespace, () => cancelled);
    listRepos({ author: namespace, limit: 100 }).then((result) => {
      if (cancelled) return;
      if (result.ok) {
        setRepos(result.data.items);
        setReposError(false);
      } else {
        // Failed, not empty: keep `repos` at null so the Select's "no
        // repositories" reading isn't confused with "couldn't check"
        // (DESIGN.md §9). The scope hint below tells the user instead.
        setRepos(null);
        setReposError(true);
      }
    });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- refreshWebhooks closes over `t`, which changing locale would otherwise needlessly refetch for.
  }, [namespace]);

  function toggleEvent(e: WebhookEvent) {
    setEvents((prev) => {
      const next = new Set(prev);
      if (next.has(e)) next.delete(e);
      else next.add(e);
      return next;
    });
  }

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!url.trim()) return;
    if (events.size === 0) {
      setCreateError(t("settings.webhooks.selectAtLeastOneEvent"));
      return;
    }
    setCreating(true);
    setCreateError(null);
    const result = await createWebhook(namespace, {
      repo: repoScope || undefined,
      url: url.trim(),
      events: Array.from(events),
    });
    setCreating(false);
    if (!result.ok) {
      setCreateError(errorMessage(t, result));
      return;
    }
    setJustCreated(result.data);
    setUrl("");
    setEvents(new Set());
    setRepoScope("");
    await refreshWebhooks(namespace);
  }

  if (user === undefined) {
    return <SkeletonLines lines={4} />;
  }
  if (needsLogin) {
    return (
      <ErrorState
        title={t("settings.webhooks.loginRequiredTitle")}
        message={t("settings.webhooks.loginRequiredMessage")}
        action={
          <Link
            href={`/login?next=${encodeURIComponent(loginNext)}`}
            className={buttonClass({ variant: "primary" })}
          >
            {t("settings.webhooks.login")}
          </Link>
        }
      />
    );
  }
  if (!user) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={t("settings.webhooks.accountLoadFailed")}
        hint={t("settings.webhooks.accountLoadFailedHint")}
      />
    );
  }

  const namespaces = adminNamespaces(user);
  if (!fixedNamespace && namespaces.length === 0) {
    return (
      <EmptyState
        icon={WebhookIcon}
        title={t("settings.webhooks.noNamespaceTitle")}
        description={t("settings.webhooks.noNamespaceDescription")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      {!fixedNamespace && (
        <Field label={t("settings.webhooks.namespaceLabel")} className="max-w-xs">
          <Select value={namespace} onChange={(e) => setNamespace(e.target.value)}>
            {namespaces.map((ns) => (
              <option key={ns} value={ns}>
                {ns}
              </option>
            ))}
          </Select>
        </Field>
      )}

      <form
        onSubmit={handleCreate}
        className="flex flex-col gap-3 rounded-lg border border-border bg-bg-sunken p-4"
      >
        <div className="flex flex-wrap gap-3">
          <Field
            label={t("settings.webhooks.scopeLabel")}
            className="min-w-[200px] flex-1"
            // A failed fetch says so instead of silently reading as "this
            // namespace has no repositories" (DESIGN.md §9) — the option
            // list would otherwise look identical to a genuinely empty one.
            hint={
              reposError ? t("settings.webhooks.scopeLoadFailed") : t("settings.webhooks.scopeHint")
            }
          >
            <Select value={repoScope} onChange={(e) => setRepoScope(e.target.value)}>
              <option value="">{t("settings.webhooks.allRepositories", { namespace })}</option>
              {(repos ?? []).map((r) => (
                <option key={`${r.kind}/${r.name}`} value={`${r.kind}/${r.name}`}>
                  {r.kind}/{r.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("settings.webhooks.urlLabel")} className="min-w-[240px] flex-[2]">
            <Input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://example.com/hooks/thinkingface"
              type="url"
              required
            />
          </Field>
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-sm font-medium text-fg-muted">
            {t("settings.webhooks.eventsLabel")}
          </span>
          <div className="flex flex-wrap gap-x-5 gap-y-1.5">
            {WEBHOOK_EVENT_OPTIONS.map((opt) => (
              <label key={opt.value} className="flex items-center gap-2 text-sm text-fg-muted">
                <Checkbox checked={events.has(opt.value)} onChange={() => toggleEvent(opt.value)} />
                {t(opt.labelKey)}
              </label>
            ))}
          </div>
        </div>
        <Button
          type="submit"
          variant="primary"
          disabled={creating || !url.trim()}
          className="self-start px-4 py-2"
        >
          {creating ? t("settings.webhooks.creating") : t("settings.webhooks.create")}
        </Button>
        {/* Below the submit button so a failed create never pushes it down
            right before the retry click (DESIGN.md §8). */}
        {createError && <Alert tone="negative">{createError}</Alert>}
      </form>

      {justCreated && (
        <Alert tone="positive" title={t("settings.webhooks.createdTitle")}>
          <p className="text-xs font-medium text-fg-subtle">
            {t("settings.webhooks.secretCopyPrefix")}
            <code className="mx-1 font-mono">X-Thinkingface-Signature</code>
            {t("settings.webhooks.secretCopySuffix")}
          </p>
          <div className="mt-1.5 flex items-center justify-between gap-2 rounded-md border border-border bg-bg-raised p-2.5">
            <code className="scroll-x whitespace-pre font-mono text-xs">{justCreated.secret}</code>
            <CopyButton value={justCreated.secret} />
          </div>
        </Alert>
      )}

      {error && <Alert tone="negative">{error}</Alert>}

      {webhooks === null && !error ? (
        <SkeletonLines lines={3} />
      ) : webhooks === null ? null : webhooks.length === 0 ? (
        <EmptyState
          icon={WebhookIcon}
          title={t("settings.webhooks.emptyTitle")}
          description={t("settings.webhooks.emptyDescription")}
        />
      ) : (
        <div className="flex flex-col gap-3">
          {webhooks.map((wh) => (
            <WebhookRow
              key={wh.id}
              webhook={wh}
              onChanged={() => refreshWebhooks(namespace)}
              onDeleted={() => refreshWebhooks(namespace)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
