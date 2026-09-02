"use client";

import {
  ArrowLeftRight,
  Building2,
  HardDrive,
  KeyRound,
  Languages,
  LogOut,
  Plus,
  Terminal,
  User as UserIcon,
  Webhook,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { Suspense, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button, buttonClass } from "@/components/ui/button";
import { useOnClickOutside } from "@/hooks/use-on-click-outside";
import { errorMessage } from "@/lib/api-error-message";
import { logout } from "@/lib/auth";
import { useT } from "@/lib/i18n/client";
import { namespaceHref } from "@/lib/namespace";
import { loginHref } from "@/lib/validation";
import type { User } from "@/types/api";

/**
 * The header's "Log in" link, pointed back at the page the reader is on.
 *
 * Split out (and Suspense-wrapped) because of `useSearchParams()`: the hook
 * opts a route into client-side rendering unless it sits under its own
 * boundary, which is the same reason `site-header.tsx` wraps `SearchBox` and
 * `MobileNav`. `UserMenu` itself is not wrapped there, so the boundary lives
 * here rather than in a file this component does not own. The fallback is the
 * same link without the `next=`, so nothing moves when it resolves.
 */
function LoginLinkWithNext({ label }: { label: string }) {
  const pathname = usePathname();
  const searchParams = useSearchParams();
  // `usePathname()` has no query string of its own, so the params are passed
  // in separately — otherwise signing in from `/datasets?search=bert&tags=nlp`
  // returns the reader to a bare `/datasets` with every filter cleared.
  return (
    <Link
      href={loginHref(pathname, searchParams.toString())}
      className={buttonClass({ variant: "primary" })}
    >
      {label}
    </Link>
  );
}

function LoginLink({ label }: { label: string }) {
  return (
    <Suspense
      fallback={
        <Link href="/login" className={buttonClass({ variant: "primary" })}>
          {label}
        </Link>
      }
    >
      <LoginLinkWithNext label={label} />
    </Suspense>
  );
}

export function UserMenu({
  user,
  pendingTransfersCount,
}: {
  user: User | null;
  /** Incoming pending transfers the viewer can accept/reject. Omitted (not 0) when unknown. */
  pendingTransfersCount?: number;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const [logoutError, setLogoutError] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  useOnClickOutside(ref, () => setOpen(false));

  if (!user) return <LoginLink label={t("userMenu.login")} />;

  async function handleLogout() {
    // Keep the menu open until this resolves: closing first means a failed
    // logout leaves the user looking at a signed-in header with no hint that
    // the session is still live.
    setLogoutError(null);
    setLoggingOut(true);
    const result = await logout();
    if (!result.ok) {
      setLoggingOut(false);
      setLogoutError(errorMessage(t, result));
      return;
    }
    setOpen(false);
    // A full document load, not `router.refresh()`. `refresh()` re-runs the
    // Server Components — which is why the header flips back to "Log in" —
    // but React deliberately *keeps* Client Component state across it, and
    // every screen under /settings/* is a client component that fetches once
    // on mount from `useEffect(…, [])`. So the previous user's access tokens,
    // SSH keys and webhooks stayed on screen, fully readable, after logging
    // out on a shared machine; only the next write revealed anything was
    // wrong (401).
    //
    // Doing it on the logout path rather than in each manager is deliberate:
    // replacing the document is the one thing that cannot leave a stale client
    // cache behind, and it covers every current and future /settings/* screen
    // at once. `replace` rather than `assign` so the authenticated page is not
    // one Back press away (bfcache can still restore a page further back in
    // the history, but every request it then makes answers 401).
    //
    // `loggingOut` is deliberately left true: the browser is navigating, and
    // re-enabling the button first only flashes a live-looking menu.
    window.location.replace("/");
  }

  return (
    <div className="relative" ref={ref}>
      <Button
        onClick={() => setOpen((v) => !v)}
        className="relative h-8 w-8 rounded-full border-transparent bg-accent-muted px-0 font-semibold text-accent-strong before:absolute before:inset-[-6px] before:content-[''] hover:bg-accent-muted"
        aria-label={t("userMenu.accountMenu", { username: user.username })}
        aria-expanded={open}
        aria-haspopup="menu"
      >
        {user.username.slice(0, 1).toUpperCase()}
      </Button>
      {open && (
        <div className="absolute right-0 top-10 w-56 rounded-lg border border-border bg-bg-raised p-1 shadow-lg">
          <div className="px-3 py-2 text-sm">
            <div className="font-medium text-fg">{user.display_name || user.username}</div>
            <div className="truncate font-mono text-xs font-medium text-fg-subtle">
              {user.username}
            </div>
            <div className="truncate text-xs font-medium text-fg-subtle">{user.email}</div>
          </div>
          <div className="my-1 h-px bg-border" />
          {/* The user's own namespace page (docs/dev/namespace-design.md §8.1) —
              first, because it is the one destination that is about them
              rather than about a setting. */}
          <Link
            href={namespaceHref(user.username)}
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <UserIcon size={14} />
            {t("namespace.yourProfile")}
          </Link>
          <Link
            href="/settings/tokens"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <KeyRound size={14} />
            {t("userMenu.accessTokens")}
          </Link>
          <Link
            href="/settings/ssh-keys"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Terminal size={14} />
            {t("userMenu.sshKeys")}
          </Link>
          <Link
            href="/settings/storage"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <HardDrive size={14} />
            {t("userMenu.storageUsage")}
          </Link>
          <Link
            href="/settings/webhooks"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Webhook size={14} />
            {t("userMenu.webhooks")}
          </Link>
          <Link
            href="/settings/transfers"
            onClick={() => setOpen(false)}
            className="flex items-center justify-between gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <span className="flex items-center gap-2">
              <ArrowLeftRight size={14} />
              {t("userMenu.transfers")}
            </span>
            {!!pendingTransfersCount && <Badge tone="accent">{pendingTransfersCount}</Badge>}
          </Link>
          <Link
            href="/settings/organizations"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Building2 size={14} />
            {t("userMenu.organizations")}
          </Link>
          <Link
            href="/orgs/new"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Plus size={14} />
            {t("userMenu.newOrganization")}
          </Link>
          <Link
            href="/settings/language"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-fg-muted hover:bg-bg-hover hover:text-fg"
          >
            <Languages size={14} />
            {t("userMenu.language")}
          </Link>
          <div className="my-1 h-px bg-border" />
          <Button
            variant="danger"
            onClick={handleLogout}
            disabled={loggingOut}
            className="w-full justify-start hover:bg-bg-hover"
          >
            <LogOut size={14} />
            {loggingOut ? t("userMenu.loggingOut") : t("userMenu.logout")}
          </Button>
          {logoutError && (
            <p role="alert" className="px-3 py-1.5 text-xs text-negative">
              {t("userMenu.logoutFailed", { message: logoutError })}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
