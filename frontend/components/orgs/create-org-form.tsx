"use client";

import { Lock } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { NamespaceAvailability } from "@/components/namespace/namespace-availability";
import { NamespaceUrlPreview } from "@/components/namespace/namespace-url-preview";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, Input, Textarea } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { namespaceHref } from "@/lib/namespace";
import { createOrg, orgErrorKey } from "@/lib/orgs";
import { type NamespaceNameError, validateNamespaceName } from "@/lib/validation";

/** Maps validateNamespaceName error codes to org-domain message keys. */
const NAME_ERROR_KEYS: Record<NamespaceNameError, MessageKey> = {
  required: "org.errors.nameRequired",
  invalid: "org.errors.nameInvalid",
  gitSuffix: "org.errors.nameGitSuffix",
  reserved: "org.errors.nameReserved",
};

export function CreateOrgForm({ loggedIn }: { loggedIn: boolean }) {
  const t = useT();
  const router = useRouter();
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    const trimmed = name.trim();
    // Checked here as well as on the server so a name the API is certain to
    // reject (bad characters, a reserved route name) costs no round trip —
    // see lib/validation.ts, mirroring backend/internal/api/names.go.
    const nameError = validateNamespaceName(trimmed);
    if (nameError) {
      setError(t(NAME_ERROR_KEYS[nameError]));
      return;
    }
    setCreating(true);
    const result = await createOrg({
      name: trimmed,
      display_name: displayName.trim() || undefined,
      description: description.trim() || undefined,
    });
    setCreating(false);
    if (!result.ok) {
      const key = orgErrorKey(result, { 401: "org.create.loginRequiredMessage" });
      setError(key ? t(key) : errorMessage(t, result));
      return;
    }
    router.push(namespaceHref(result.data.org.name));
    router.refresh();
  }

  const preview = name.trim() || t("org.create.namePlaceholder");

  return (
    <form onSubmit={handleSubmit} className="flex max-w-lg flex-col gap-4">
      <Field
        label={t("org.create.idLabel")}
        hint={t("org.create.idHint", { example: `${preview}/my-model` })}
      >
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("org.create.namePlaceholder")}
          required
        />
      </Field>

      <div className="flex flex-col gap-1.5">
        <NamespaceUrlPreview name={name} placeholder={t("org.create.namePlaceholder")} />
        <NamespaceAvailability name={name} errorKeys={NAME_ERROR_KEYS} />
      </div>

      <Field label={t("org.create.displayNameLabel")} hint={t("org.create.displayNameHint")}>
        <Input
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder={t("org.create.displayNamePlaceholder")}
        />
      </Field>

      <Field label={t("org.create.descriptionLabel")}>
        <Textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          className="min-h-20"
          placeholder={t("org.create.descriptionPlaceholder")}
        />
      </Field>

      <p className="flex items-start gap-1.5 text-xs font-medium text-fg-subtle">
        <Lock size={13} className="mt-0.5 shrink-0" aria-hidden="true" />
        {t("org.create.idPermanent")}
      </p>

      <Button
        type="submit"
        variant="primary"
        disabled={creating || !loggedIn}
        className="self-start px-4 py-2"
      >
        {creating ? t("org.create.submitting") : t("org.create.submit")}
      </Button>

      {/* Below the submit button so a failed create never pushes it out from
          under the pointer right before the retry click (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
    </form>
  );
}
