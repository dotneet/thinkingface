// newRepo: the new repository creation page.
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const newRepo = {
  title: "New repository",
  blurb: "Create a dataset or model repository. You can push files with git afterwards.",
  loginNotice: {
    prefix: "You need to be logged in to create a repository. ",
    link: "Log in",
    suffix: " first.",
  },
  // A backend that answered with an error, or did not answer at all, is not
  // the same thing as being signed out (DESIGN.md §9) — this is what the
  // failure says instead of the login notice above.
  accountLoadFailed: "Couldn't check which namespaces you can create in.",
  accountLoadFailedHint: "The backend API may be unreachable. Try reloading the page.",
  kind: {
    dataset: "dataset",
    model: "model",
  },
  namespace: "Namespace",
  namespacePlaceholder: "your-username",
  // Namespace kind badge on the picker.
  kindUser: "personal",
  kindOrg: "organization",
  name: "Name",
  nameHint:
    "1–96 characters: letters, digits, dot, dash or underscore. Must start with a letter or digit and must not end in .git.",
  namePlaceholder: "my-dataset",
  description: "Description",
  descriptionPlaceholder: "What is this repository for?",
  create: "Create repository",
  creating: "Creating…",
  errors: {
    namespaceRequired: "Namespace is required.",
    loginRequired: "You need to be logged in to create a repository. Log in and try again.",
    nameRequired: "Repository name is required.",
    nameInvalid:
      "Repository name must be 1-96 characters of letters, digits, dot, dash or underscore, and start with a letter or digit.",
    nameGitSuffix: "Repository name must not end in .git.",
  },
};
