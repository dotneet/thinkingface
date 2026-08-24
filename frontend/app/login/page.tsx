import type { Metadata } from "next";
import { titleMetadata } from "@/app/page-metadata";
import { LoginForm } from "@/components/auth/login-form";
import { getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return titleMetadata(t("meta.signIn"));
}

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ next?: string }>;
}) {
  const [{ next }, t] = await Promise.all([searchParams, getT()]);
  return (
    <div className="py-12">
      <h1 className="mb-8 text-center text-2xl font-semibold tracking-tight">
        {t("auth.welcome")}
      </h1>
      <LoginForm next={next} />
    </div>
  );
}
