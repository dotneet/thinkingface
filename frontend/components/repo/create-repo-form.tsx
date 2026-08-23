"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { cn } from "@/lib/cn";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { orgErrorKey } from "@/lib/orgs";
import { createRepo } from "@/lib/repos";
import { type NameError, validateName } from "@/lib/validation";
import type { Namespace, RepoKind } from "@/types/api";

/** Maps validateName error codes to newRepo-domain message keys. */
const NAME_ERROR_KEYS: Record<NameError, MessageKey> = {
  required: "newRepo.errors.nameRequired",
  invalid: "newRepo.errors.nameInvalid",
  gitSuffix: "newRepo.errors.nameGitSuffix",
};

const KIND_KEYS: Record<RepoKind, MessageKey> = {
  dataset: "newRepo.kind.dataset",
  model: "newRepo.kind.model",
};

export function CreateRepoForm({
  namespaces,
  loggedIn,
  initialNamespace,
}: {
  /** Where the user may create: their own name plus every org they can write to. */
  namespaces: Namespace[];
  loggedIn: boolean;
  /**
   * Preselected namespace from `/new?ns=` (docs/namespace-design.md §4.3).
   * Ignored unless the viewer may actually create there, so a hand-edited URL
   * cannot put the form into a state the server would reject.
   */
  initialNamespace?: string;
}) {
  const t = useT();
  const router = useRouter();
  const [kind, setKind] = useState<RepoKind>("dataset");
  const [namespace, setNamespace] = useState(
    // Case-insensitive, like every namespace lookup: `/new?ns=Alice` still
    // selects `alice`. The stored value is the canonical spelling.
    namespaces.find((ns) => ns.name.toLowerCase() === initialNamespace?.toLowerCase())?.name ??
      namespaces[0]?.name ??
      "",
  );
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  // Drives the namespace badge below the form fields.
  const selected = namespaces.find((ns) => ns.name === namespace);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    const ns = namespace.trim();
    const nm = name.trim();
    if (!ns) {
      setError(t("newRepo.errors.namespaceRequired"));
      return;
    }
    // Check before sending so a name the API is certain to reject costs no
    // round trip (validateName in backend/internal/api/repos.go).
    const nameError = validateName(nm);
    if (nameError) {
      setError(t(NAME_ERROR_KEYS[nameError]));
      return;
    }
    setLoading(true);
    const result = await createRepo({
      kind,
      namespace: ns,
      name: nm,
      description,
    });
    setLoading(false);
    if (!result.ok) {
      if (isUnauthorized(result)) {
        setError(t("newRepo.errors.loginRequired"));
        return;
      }
      // Maps the backend's error.type (e.g. reserved_name) to localized copy;
      // anything not in the dictionary falls back to the server's message.
      const key = orgErrorKey(result);
      setError(key ? t(key) : errorMessage(t, result));
      return;
    }
    router.push(`/${kind}s/${encodeURIComponent(ns)}/${encodeURIComponent(nm)}`);
  }

  return (
    <form onSubmit={handleSubmit} className="flex max-w-lg flex-col gap-4">
      <div className="flex gap-2">
        {(["dataset", "model"] as RepoKind[]).map((k) => (
          <Button
            key={k}
            onClick={() => setKind(k)}
            // The selection is otherwise carried by colour alone, which no
            // screen reader relays.
            aria-pressed={kind === k}
            className={cn(
              "flex-1 py-2 capitalize",
              kind === k &&
                "border-accent bg-accent-muted text-accent-strong hover:bg-accent-muted",
            )}
          >
            {t(KIND_KEYS[k])}
          </Button>
        ))}
      </div>

      <div className="flex gap-2">
        <Field label={t("newRepo.namespace")} className="flex-1">
          {namespaces.length > 0 ? (
            <Select value={namespace} onChange={(e) => setNamespace(e.target.value)}>
              {namespaces.map((ns) => (
                <option key={ns.name} value={ns.name}>
                  {/* An <option> can hold only text, so the kind travels as a
                      suffix here and as a Badge below the field. */}
                  {ns.name} · {t(ns.kind === "org" ? "newRepo.kindOrg" : "newRepo.kindUser")}
                </option>
              ))}
            </Select>
          ) : (
            <Input
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              placeholder={t("newRepo.namespacePlaceholder")}
            />
          )}
        </Field>
        <Field label={t("newRepo.name")} className="flex-1" hint={t("newRepo.nameHint")}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("newRepo.namePlaceholder")}
            required
          />
        </Field>
      </div>

      {selected && (
        <div className="-mt-2 flex items-center gap-2 text-xs font-medium text-fg-subtle">
          <Badge tone={selected.kind === "org" ? "accent" : "muted"}>
            {t(selected.kind === "org" ? "newRepo.kindOrg" : "newRepo.kindUser")}
          </Badge>
          <span className="font-mono">
            {namespace}/{name.trim() || t("newRepo.namePlaceholder")}
          </span>
        </div>
      )}

      <Field label={t("newRepo.description")}>
        <Textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          className="min-h-20"
          placeholder={t("newRepo.descriptionPlaceholder")}
        />
      </Field>

      <Button
        type="submit"
        variant="primary"
        disabled={loading || !loggedIn}
        className="self-start px-4 py-2"
      >
        {loading ? t("newRepo.creating") : t("newRepo.create")}
      </Button>

      {/* Below the submit button so a failed create never pushes it out from
          under the pointer right before the retry click (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
    </form>
  );
}
