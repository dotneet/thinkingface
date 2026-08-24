// The Japanese dictionary. Typed against `Dict` (en's shape), so any key
// mismatch is caught by typecheck.
import type { Dict } from "@/lib/i18n/dictionaries/en";
import { auth } from "@/lib/i18n/dictionaries/ja/auth";
import { common } from "@/lib/i18n/dictionaries/ja/common";
import { errors } from "@/lib/i18n/dictionaries/ja/errors";
import { experiments } from "@/lib/i18n/dictionaries/ja/experiments";
import { home } from "@/lib/i18n/dictionaries/ja/home";
import { meta } from "@/lib/i18n/dictionaries/ja/meta";
import { model } from "@/lib/i18n/dictionaries/ja/model";
import { namespace } from "@/lib/i18n/dictionaries/ja/namespace";
import { newRepo } from "@/lib/i18n/dictionaries/ja/newRepo";
import { org } from "@/lib/i18n/dictionaries/ja/org";
import { parquet } from "@/lib/i18n/dictionaries/ja/parquet";
import { repo } from "@/lib/i18n/dictionaries/ja/repo";
import { repoList } from "@/lib/i18n/dictionaries/ja/repoList";
import { settings } from "@/lib/i18n/dictionaries/ja/settings";
import { settingsDetail } from "@/lib/i18n/dictionaries/ja/settingsDetail";
import { ui } from "@/lib/i18n/dictionaries/ja/ui";

export const ja: Dict = {
  ...common,
  auth,
  errors,
  experiments,
  home,
  meta,
  model,
  namespace,
  newRepo,
  org,
  parquet,
  repo,
  repoList,
  settings,
  settingsDetail,
  ui,
};
