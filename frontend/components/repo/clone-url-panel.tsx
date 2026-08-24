"use client";

import { useState } from "react";
import { CodeBlock } from "@/components/ui/code-block";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { useT } from "@/lib/i18n/client";

type Protocol = "http" | "ssh";

/**
 * The `git clone` block, with the SSH remote alongside the HTTP one.
 *
 * `/settings/ssh-keys` has always let people register a key, but no screen
 * ever showed a URL that key could be used against — and the SSH port is
 * deployment-specific (`TF_SSH_ADDR`), so it cannot be guessed from the page.
 *
 * When the server has SSH turned off it sends `ssh_clone_url` as an empty
 * string, and the switch is not rendered at all: an SSH tab holding nothing
 * would be an empty value dressed up as a real one (DESIGN.md §9).
 */
export function CloneUrlPanel({
  cloneUrl,
  sshCloneUrl,
}: {
  cloneUrl: string;
  /** Empty when the server runs without the git-over-SSH listener. */
  sshCloneUrl: string;
}) {
  const t = useT();
  const [protocol, setProtocol] = useState<Protocol>("http");

  const sshAvailable = sshCloneUrl !== "";
  const active = sshAvailable && protocol === "ssh" ? "ssh" : "http";
  const url = active === "ssh" ? sshCloneUrl : cloneUrl;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
          {t("repo.clone.title")}
        </span>
        {sshAvailable && (
          <SegmentedControl
            value={active}
            onChange={setProtocol}
            label={t("repo.clone.protocolLabel")}
            options={[
              { value: "http", label: t("repo.clone.http") },
              { value: "ssh", label: t("repo.clone.ssh") },
            ]}
          />
        )}
      </div>
      {/* The command itself is an identifier, not copy: never translated. */}
      <CodeBlock value={`git clone ${url}`} />
      {active === "ssh" && (
        <>
          <p className="text-xs font-medium text-fg-subtle">{t("repo.clone.sshHint")}</p>
          <p className="text-xs font-medium text-fg-subtle">{t("repo.clone.sshLfsHint")}</p>
        </>
      )}
    </div>
  );
}
