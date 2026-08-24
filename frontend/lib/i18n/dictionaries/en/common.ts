// Shell (header / nav / user menu) and the language settings page.
// NOTE: the en dictionary is the source of truth for shape, so it must not be
// `as const` (doing so would turn it into literal types and force ja to use
// the exact same strings).
export const common = {
  nav: {
    datasets: "Datasets",
    models: "Models",
    experiments: "Experiments",
  },
  header: {
    new: "New",
    newRepository: "New repository",
    openMenu: "Open menu",
    closeMenu: "Close menu",
    searchModels: "Search models...",
    searchDatasets: "Search datasets...",
    searchExperiments: "Search experiments...",
  },
  theme: {
    light: "Light theme",
    dark: "Dark theme",
    system: "System theme",
    toggle: "Theme: {label}. Click to change.",
  },
  userMenu: {
    login: "Log in",
    accountMenu: "Account menu for {username}",
    accessTokens: "Access tokens",
    sshKeys: "SSH keys",
    storageUsage: "Storage usage",
    webhooks: "Webhooks",
    transfers: "Repository transfers",
    organizations: "Organizations",
    newOrganization: "New organization",
    language: "Language",
    logout: "Log out",
    loggingOut: "Logging out…",
    logoutFailed: "Couldn't log out: {message}",
  },
  language: {
    title: "Language",
    description: "Choose the display language for the web interface.",
    groupLabel: "Display language",
    auto: "Follow browser setting",
    autoHint: "Uses the language your browser requests. Currently: {resolved}.",
    en: "English",
    ja: "日本語",
  },
};
