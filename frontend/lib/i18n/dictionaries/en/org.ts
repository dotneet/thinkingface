// Organisations: the public directory, the creation form, an organisation's
// profile page, and its settings screens (docs/dev/organization-design.md §8).
// NOTE: the en dictionary is the source of truth for shape, so it must not be
// `as const` (doing so would turn it into literal types and force ja to use
// the exact same strings).
export const org = {
  roles: {
    admin: "Admin",
    write: "Write",
    read: "Read",
    none: "Not a member",
  },
  errors: {
    // validateNamespaceName's codes, mapped for both the create form and sign-up.
    nameRequired: "Enter a name.",
    nameInvalid:
      "Use letters, digits, dot, dash, or underscore; start with a letter or digit (96 characters max).",
    nameGitSuffix: "A name can't end in .git.",
    nameReserved: "That name is reserved by this server. Pick another.",
    creationDisabled: "Only a site administrator can create organizations on this server.",
    alreadyMember: "That user is already a member of this organization.",
    lastAdmin: "An organization needs at least one admin. Promote someone else first.",
    hasRepositories: "Delete or transfer the remaining repositories before deleting this org.",
    userNotFound: "No user with that username.",
    permissionDenied: "You don't have permission to do that.",
    loginRequired: "Log in to continue.",
  },
  directory: {
    title: "Organizations",
    blurb: "Shared namespaces for teams: repositories, members, and policy in one place.",
    searchPlaceholder: "Search organizations...",
    search: "Search",
    create: "New organization",
    emptyTitle: "No organizations yet",
    emptyDescription: "Create one to share a namespace with your team.",
    noMatchesTitle: "No organizations match",
    noMatchesDescription: "Try a different search term.",
    clearSearch: "Clear search",
    countOne: "{count} organization",
    countOther: "{count} organizations",
    removeSearchAria: "Remove search filter: {value}",
    loadFailedHint: "The backend API may be unreachable. Try reloading the page.",
    membersOne: "{count} member",
    membersOther: "{count} members",
    reposOne: "{count} repository",
    reposOther: "{count} repositories",
  },
  create: {
    title: "New organization",
    blurb: "An organization owns repositories under its own namespace. You become its first admin.",
    // Namespace-design.md §5.2: the ID claims the organization's namespace
    // at /{id}, same treatment as the sign-up username (§5.1).
    idLabel: "Organization ID (namespace)",
    idHint: "Used in URLs and repository names: {example}. It can't be changed later.",
    idPermanent: "Organization IDs are permanent. The display name can be changed any time.",
    namePlaceholder: "acme-research",
    displayNameLabel: "Display name",
    displayNameHint:
      "Optional, and can be changed any time — shown instead of the ID on the organization page.",
    displayNamePlaceholder: "Acme Research",
    descriptionLabel: "Description",
    descriptionPlaceholder: "What this organization works on.",
    submit: "Create organization",
    submitting: "Creating…",
    loginRequiredTitle: "Login required",
    loginRequiredMessage: "You need to be logged in to create an organization.",
    login: "Log in",
  },
  page: {
    loadFailedTitle: "Couldn't load this",
    notFoundTitle: "Organization not found",
    notFoundDescription: "No organization is registered under {name}.",
    loadFailedHint: "The backend API may be unreachable. Try reloading the page.",
    membersTitle: "Members",
    membersHiddenTitle: "Members are private",
    membersHiddenDescription: "Only members of this organization can see who belongs to it.",
    membersEmptyTitle: "No members",
    membersEmptyDescription: "An admin can add people from the organization's settings.",
    membersMore: "+{count} more",
    manageMembers: "Manage members",
  },
  settings: {
    title: "Organization settings",
    subtitle: "Profile, members, and policy for {name}.",
    backToOrg: "Back to {name}",
    noPermissionTitle: "Admins only",
    noPermissionMessage: "You need the admin role in {name} to open its settings.",
    loadFailedHint: "The backend API may be unreachable. Try reloading the page.",
    navProfile: "Profile",
    navMembers: "Members",
    navWebhooks: "Webhooks",
    navStorage: "Storage",
    navAuditLog: "Audit log",
    navDanger: "Delete organization",
    profile: {
      title: "Profile",
      description: "How this organization is presented on its public page.",
      nameLabel: "Name",
      nameHint: "The namespace name can't be changed.",
      displayNameLabel: "Display name",
      descriptionLabel: "Description",
      websiteLabel: "Website",
      websitePlaceholder: "https://example.com",
      avatarUrlLabel: "Avatar URL",
      avatarUrlHint: "A link to an image hosted elsewhere; uploads aren't supported.",
      avatarPlaceholder: "https://example.com/logo.png",
      save: "Save changes",
      saving: "Saving…",
      saved: "Changes saved.",
    },
    policy: {
      title: "Policy",
      description: "Rules applied to this organization.",
      membersVisibilityLabel: "Member list",
      membersVisibilityMembers: "Visible to members",
      membersVisibilityPublic: "Visible to everyone",
      membersVisibilityHint: "Who may see the list of people in this organization.",
    },
    members: {
      title: "Members",
      description: "Roles apply to every repository in the organization.",
      addTitle: "Add a member",
      usernameLabel: "Username",
      usernamePlaceholder: "alice",
      roleLabel: "Role",
      add: "Add member",
      adding: "Adding…",
      colUsername: "User",
      colRole: "Role",
      colJoined: "Joined",
      colActions: "Actions",
      you: "you",
      remove: "Remove",
      removing: "Removing…",
      confirmRemoveTitle: "Remove {username}?",
      confirmRemove:
        "Remove {username} from {org}? They lose membership and the ability to push to its repositories.",
      changingRole: "Changing…",
      // A privilege change is confirmed the way the admin screens confirm one:
      // the trigger here is a <select>, where a stray scroll or a mis-release
      // would otherwise write a new role with no prompt.
      confirmRoleTitle: "Change {username}'s role?",
      confirmRole: "Change {username}'s role in {org} from {from} to {to}.",
      confirmRoleAdmin:
        "Admins can add and remove members, change everyone's role, and manage the organization's settings and webhooks.",
      // Shown only when the row being changed/removed belongs to the viewer
      // themselves. The dialog otherwise reads like it is about someone
      // else, and a role change or removal that succeeds against your own
      // account starts 403ing every control on this screen.
      confirmRoleSelfWarning:
        "This changes your own role. If it no longer allows managing members, you will immediately lose access to this settings page.",
      confirmRemoveSelfWarning:
        "This removes you from {org}. You will immediately lose access to this organization's settings.",
      confirmRoleConfirm: "Change role",
      emptyTitle: "No members",
      emptyDescription: "Add someone by username to give them access.",
      loadFailed: "Failed to load members",
      loadFailedHint: "The backend API may be unreachable. Try reloading the page.",
      roleHint:
        "Read grants membership only, write can push to the organization's repositories, and admin also manages members and settings.",
    },
    webhooks: {
      title: "Webhooks",
      description:
        "Endpoints notified about repositories in this organization. Only admins can manage them.",
    },
    storage: {
      title: "Storage usage",
      description: "GCS-billed LFS bytes held by this organization's repositories.",
      emptyTitle: "No storage usage yet",
      emptyDescription: "Push an LFS-tracked file to a repository here to see usage.",
    },
    auditLog: {
      title: "Audit log",
      description: "Administrative changes and repository lifecycle events, newest first.",
      colWhen: "When",
      colActor: "Actor",
      colAction: "Action",
      colTarget: "Target",
      unknownActor: "(deleted user)",
      loadMore: "Load more",
      loadingMore: "Loading…",
      emptyTitle: "Nothing recorded yet",
      emptyDescription: "Member changes and repository events will show up here.",
      loadFailed: "Failed to load the audit log",
      loadFailedHint: "The backend API may be unreachable. Try reloading the page.",
    },
    danger: {
      title: "Delete organization",
      description:
        "Deleting an organization removes its members, webhooks, and audit log. The name becomes available again.",
      blockedTitleOne: "{count} repository still belongs to {name}",
      blockedTitleOther: "{count} repositories still belong to {name}",
      blockedDescription:
        "Delete or transfer them first — deleting an organization never deletes repositories.",
      viewRepositories: "View repositories",
      delete: "Delete this organization",
      deleting: "Deleting…",
      confirmTitle: "Delete {name}?",
      confirmWarningTitle: "This can't be undone",
      confirmWarning:
        "{name}'s members, webhooks, and audit log will be removed permanently. The name becomes available again.",
      confirmCancel: "Cancel",
      confirmSubmit: "Delete organization",
    },
  },
};
