import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { SSHKeysManager } from "@/components/settings/ssh-keys-manager";
import { getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.settings"), t("meta.sshKeys"));
}

export default async function SSHKeysPage() {
  const t = await getT();
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("settings.sshKeys.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">
          {t("settings.sshKeys.descriptionPrefix")}
          <code className="font-mono text-xs">ssh://git@host:2222/&lt;ns&gt;/&lt;name&gt;.git</code>
          {t("settings.sshKeys.descriptionSuffix")}
        </p>
      </div>
      <SSHKeysManager />
    </div>
  );
}
