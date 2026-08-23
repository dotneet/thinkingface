import { LoaderCircle } from "lucide-react";
import { Alert } from "@/components/ui/alert";
import { getT } from "@/lib/i18n/server";

export async function IndexingBanner() {
  const t = await getT();
  return (
    <Alert tone="warning" icon={LoaderCircle}>
      {t("repo.indexing.message")}
    </Alert>
  );
}
