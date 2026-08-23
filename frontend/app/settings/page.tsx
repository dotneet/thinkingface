import { redirect } from "next/navigation";

/** `/settings` has no content of its own; send visitors to the first tab. */
export default function SettingsIndexPage() {
  redirect("/settings/profile");
}
