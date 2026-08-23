import type { Metadata } from "next";
import { notFound, permanentRedirect } from "next/navigation";
import { NamespaceOverview, type NamespaceSearch } from "@/components/namespace/namespace-overview";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { getNamespace, isReservedNamespace, namespaceHref } from "@/lib/namespace";
import { decodeRouteParams } from "@/lib/paths";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string }>;
}): Promise<Metadata> {
  const { ns } = decodeRouteParams(await params);
  const result = await getNamespace(ns, { headers: await authHeaders() });
  if (!result.ok) return { title: `${ns} · 🤔 Thinking Face` };
  const { name, display_name, description } = result.data.namespace;
  return {
    title: `${display_name || name} · 🤔 Thinking Face`,
    description: description || undefined,
  };
}

/** Rebuilds the query string so a canonical-spelling redirect keeps the tab,
 * filters and paging the visitor arrived with. */
function queryString(sp: NamespaceSearch): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(sp)) {
    if (value === undefined) continue;
    for (const v of Array.isArray(value) ? value : [value]) params.append(key, v);
  }
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export default async function NamespacePage({
  params,
  searchParams,
}: {
  params: Promise<{ ns: string }>;
  searchParams: Promise<NamespaceSearch>;
}) {
  const { ns } = decodeRouteParams(await params);
  // Defense in depth: Next.js already prefers the static `/models`,
  // `/datasets`, … routes over this `[ns]` catch-all, but reject known
  // reserved segments explicitly in case a future top-level route is added
  // without a matching entry (see lib/validation.ts).
  if (isReservedNamespace(ns)) notFound();

  // Forwarded so viewer_role / can_edit and the member list reflect the
  // signed-in user (see lib/server-auth.ts).
  const [result, t] = await Promise.all([
    getNamespace(ns, { headers: await authHeaders() }),
    getT(),
  ]);

  // A namespace that exists but owns nothing is a 200 with zero counts; only
  // a name nobody holds is a 404 (docs/namespace-design.md §5.5).
  if (isNotFound(result)) notFound();
  if (!result.ok) {
    return (
      <ErrorState
        title={t("namespace.errorTitle")}
        message={errorMessage(t, result)}
        hint={t("namespace.backendHint")}
      />
    );
  }

  const profile = result.data.namespace;
  // Namespace names are case-insensitive, so `/Alice` resolves to `alice`.
  // Send the visitor to the one canonical URL rather than serving the same
  // profile under every spelling (§4.1).
  if (profile.name !== ns) {
    permanentRedirect(`${namespaceHref(profile.name)}${queryString(await searchParams)}`);
  }

  return <NamespaceOverview profile={profile} searchParams={searchParams} />;
}
