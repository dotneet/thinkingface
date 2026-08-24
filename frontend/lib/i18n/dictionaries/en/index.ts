// The English dictionary. It is the single source of truth for shape (key
// structure); the `Dict` type forces ja to match the same shape. Copy is
// split into per-domain files so that adding to your area only requires
// editing that one file.
import { auth } from "@/lib/i18n/dictionaries/en/auth";
import { common } from "@/lib/i18n/dictionaries/en/common";
import { errors } from "@/lib/i18n/dictionaries/en/errors";
import { experiments } from "@/lib/i18n/dictionaries/en/experiments";
import { home } from "@/lib/i18n/dictionaries/en/home";
import { meta } from "@/lib/i18n/dictionaries/en/meta";
import { model } from "@/lib/i18n/dictionaries/en/model";
import { namespace } from "@/lib/i18n/dictionaries/en/namespace";
import { newRepo } from "@/lib/i18n/dictionaries/en/newRepo";
import { org } from "@/lib/i18n/dictionaries/en/org";
import { parquet } from "@/lib/i18n/dictionaries/en/parquet";
import { repo } from "@/lib/i18n/dictionaries/en/repo";
import { repoList } from "@/lib/i18n/dictionaries/en/repoList";
import { settings } from "@/lib/i18n/dictionaries/en/settings";
import { settingsDetail } from "@/lib/i18n/dictionaries/en/settingsDetail";
import { ui } from "@/lib/i18n/dictionaries/en/ui";

export const en = {
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

export type Dict = typeof en;
