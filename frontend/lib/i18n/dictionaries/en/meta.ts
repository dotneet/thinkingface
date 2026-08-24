// meta: page <title> copy produced by each route's `generateMetadata`.
//
// Kept out of the per-screen dictionaries on purpose: a title is written for a
// browser tab and a link preview, not for the page body, so it reads
// differently from the heading of the same screen and is easier to keep
// consistent when every one of them sits together.
//
// Only the *words around* the subject live here. A repository, namespace,
// project, run or file path is an identifier and is never translated
// (DESIGN.md §7) — it goes into the title verbatim, straight from the route
// params.
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const meta = {
  /** Site-wide `<meta name="description">`, rendered by the root layout. */
  description: "Datasets, models, and experiment tracking.",

  // Listings.
  models: "Models",
  datasets: "Datasets",
  organizations: "Organizations",
  experiments: "Experiments",

  // Repository sub-pages, named after the tab that leads to them.
  files: "Files",
  commits: "Commits",
  edit: "Edit",
  viewer: "Viewer",
  settings: "Settings",

  // Account / organisation settings screens.
  profile: "Profile",
  account: "Account",
  tokens: "Access tokens",
  sshKeys: "SSH keys",
  storage: "Storage",
  webhooks: "Webhooks",
  transfers: "Transfers",
  language: "Language",
  members: "Members",
  auditLog: "Audit log",
  dangerZone: "Danger zone",

  // Creation and sign-in.
  newRepository: "New repository",
  newOrganization: "New organization",
  signIn: "Sign in",
};
