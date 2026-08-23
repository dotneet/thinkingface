import { TokensManager } from "@/components/settings/tokens-manager";
import { getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export default async function TokensPage() {
  const t = await getT();
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("settings.tokens.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">
          {t("settings.tokens.descriptionPrefix")}
          <code className="font-mono text-xs">Authorization: Bearer tf_...</code>
          {t("settings.tokens.descriptionSuffix")}
        </p>
      </div>
      <TokensManager />
    </div>
  );
}
