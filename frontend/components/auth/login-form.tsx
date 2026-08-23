"use client";

import { Lock } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { NamespaceAvailability } from "@/components/namespace/namespace-availability";
import { NamespaceUrlPreview } from "@/components/namespace/namespace-url-preview";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { login, signup } from "@/lib/auth";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { type NamespaceNameError, safeRedirectPath, validateNamespaceName } from "@/lib/validation";

type Tab = "login" | "signup";

/** Maps validateNamespaceName error codes to auth-domain message keys. */
const USERNAME_ERROR_KEYS: Record<NamespaceNameError, MessageKey> = {
  required: "auth.errors.usernameRequired",
  invalid: "auth.errors.usernameInvalid",
  gitSuffix: "auth.errors.usernameGitSuffix",
  reserved: "auth.errors.usernameReserved",
};

export function LoginForm({ next }: { next?: string }) {
  const t = useT();
  const [tab, setTab] = useState<Tab>("login");
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const router = useRouter();
  const destination = safeRedirectPath(next);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (tab === "signup") {
      // Same rule the API applies to a new username: the shared name syntax
      // plus the reserved-namespace list, because signing up claims a
      // namespace (backend/internal/api/names.go, docs/dev/organization-design.md §6.3).
      const nameError = validateNamespaceName(username.trim());
      if (nameError) {
        setError(t(USERNAME_ERROR_KEYS[nameError]));
        return;
      }
      if (password.length < 8) {
        setError(t("auth.errors.passwordTooShort"));
        return;
      }
    }
    setLoading(true);
    const result =
      tab === "login" ? await login(username, password) : await signup(username, email, password);
    setLoading(false);
    if (!result.ok) {
      // The backend answers a failed login with the generic "unauthorized"
      // error type (backend/internal/api/auth.go's handleLogin), which
      // would otherwise render as "you need to be logged in" — misleading
      // for a login attempt that just failed (see [S12]).
      setError(
        tab === "login" && result.status === 401
          ? t("auth.errors.invalidCredentials")
          : errorMessage(t, result),
      );
      return;
    }
    router.push(destination);
    router.refresh();
  }

  return (
    <div className="mx-auto w-full max-w-sm">
      <div className="mb-6 flex rounded-md border border-border p-1">
        {(["login", "signup"] as Tab[]).map((tabId) => (
          <Button
            key={tabId}
            variant={tab === tabId ? "primary" : "ghost"}
            aria-pressed={tab === tabId}
            onClick={() => {
              setTab(tabId);
              setError(null);
            }}
            className="flex-1 rounded"
          >
            {tabId === "login" ? t("auth.loginTab") : t("auth.signupTab")}
          </Button>
        ))}
      </div>

      <form onSubmit={handleSubmit} className="flex flex-col gap-3">
        <Field
          label={tab === "signup" ? t("auth.usernameLabel") : t("auth.username")}
          hint={tab === "signup" ? t("auth.usernameHint") : undefined}
        >
          <Input
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            placeholder={tab === "signup" ? t("auth.usernamePlaceholder") : undefined}
          />
        </Field>

        {tab === "signup" && (
          <div className="flex flex-col gap-1.5">
            <NamespaceUrlPreview name={username} placeholder={t("auth.usernamePlaceholder")} />
            <NamespaceAvailability name={username} errorKeys={USERNAME_ERROR_KEYS} />
          </div>
        )}

        {tab === "signup" && (
          <Field label={t("auth.email")}>
            <Input
              required
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="email"
            />
          </Field>
        )}

        <Field label={t("auth.password")}>
          <Input
            required
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={tab === "login" ? "current-password" : "new-password"}
            minLength={tab === "signup" ? 8 : undefined}
          />
        </Field>

        {tab === "signup" && (
          <p className="flex items-start gap-1.5 text-xs font-medium text-fg-subtle">
            <Lock size={13} className="mt-0.5 shrink-0" aria-hidden="true" />
            {t("auth.usernamePermanent")}
          </p>
        )}

        <Button type="submit" variant="primary" disabled={loading} className="mt-2 py-2">
          {loading
            ? t("auth.pleaseWait")
            : tab === "login"
              ? t("auth.submitLogin")
              : t("auth.submitSignup")}
        </Button>

        {/* Below the submit button, not above it — an error appearing here
            must never push the button the user is about to click again
            (DESIGN.md §8). */}
        {error && <Alert tone="negative">{error}</Alert>}
      </form>
    </div>
  );
}
