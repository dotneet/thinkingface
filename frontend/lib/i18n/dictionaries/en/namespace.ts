// namespace: the /{ns} profile page shared by users and organizations
// (docs/namespace-design.md §8.3).
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const namespace = {
  kind: {
    user: "User",
    org: "Organization",
  },
  tabs: {
    models: "Models",
    datasets: "Datasets",
    experiments: "Experiments",
    members: "Members",
  },
  counts: {
    modelsOne: "{count} model",
    modelsOther: "{count} models",
    datasetsOne: "{count} dataset",
    datasetsOther: "{count} datasets",
    membersOne: "{count} member",
    membersOther: "{count} members",
  },
  joinedOn: "Joined {date}",
  editProfile: "Edit profile",
  settings: "Settings",
  yourProfile: "Your profile",
  empty: {
    models: "No models here yet",
    datasets: "No datasets here yet",
    experiments: "No experiment repositories here yet",
    ownModels: "You haven't published any models here yet",
    ownDatasets: "You haven't published any datasets here yet",
    ownExperiments: "No experiments have been tracked here yet",
    createFirstDescription:
      "Repositories you create here are named {ns}/<repo> and can be cloned with git.",
    createFirst: "Create the first repository",
  },
  errorTitle: "Couldn't load this",
  backendHint: "The backend API may not be running. Check API_URL / NEXT_PUBLIC_API_URL.",
};
