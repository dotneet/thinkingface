// home: the top page and not-found.
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const home = {
  tagline:
    "A home for your team's datasets, model checkpoints, and experiment runs — backed by Git, LFS, and Google Cloud Storage.",
  stats: {
    datasets: "Datasets",
    models: "Models",
    experiments: "Experiments",
    totalStorage: "Total storage",
  },
  recentDatasets: "Recently updated datasets",
  recentModels: "Recently updated models",
  viewAll: "View all",
  noDatasets: "No datasets yet",
  noModels: "No models yet",
  backendHint: "The backend API may not be running. Check API_URL / NEXT_PUBLIC_API_URL.",
  notFound: {
    title: "Not found",
    description: "This page, repository, or file doesn't exist.",
    goHome: "Go home",
    login: "Log in",
  },
};
