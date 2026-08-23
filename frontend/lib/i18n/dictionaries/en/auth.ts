// auth: login / sign-up.
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const auth = {
  welcome: "Welcome to 🤔 Thinking Face",
  loginTab: "Log in",
  signupTab: "Sign up",
  username: "Username",
  email: "Email",
  password: "Password",
  // Sign-up only (docs/dev/namespace-design.md §5.1): claiming a username also
  // claims the namespace at /{username}, so the label and hint say so.
  usernameLabel: "Username (your namespace)",
  usernameHint:
    "Also your namespace: your profile is /{username} and repositories are {username}/<repo>. It can't be changed later. Letters, digits, dot, dash or underscore; 1–96 characters.",
  usernamePlaceholder: "alice",
  usernamePermanent: "Usernames are permanent. You can change your display name any time.",
  submitLogin: "Log in",
  submitSignup: "Create account",
  pleaseWait: "Please wait…",
  // NamespaceUrlPreview row labels (components/namespace/namespace-url-preview.tsx),
  // shared by sign-up and organisation creation.
  preview: {
    profileLabel: "Profile",
    repositoriesLabel: "Repositories",
  },
  // NamespaceAvailability's checking/available/taken states
  // (components/namespace/namespace-availability.tsx), shared by sign-up and
  // organisation creation.
  availability: {
    checking: "Checking availability…",
    available: "{name} is available",
    taken: "{name} is already taken (case-insensitive)",
  },
  errors: {
    // The backend answers a failed login with the generic "unauthorized"
    // error type (see backend/internal/api/auth.go's handleLogin), which
    // lib/api-error-message.ts's generic mapping renders as "you need to be
    // logged in" — misleading for a login attempt that just failed. This
    // overrides that one case (see [S12]).
    invalidCredentials: "Username or password is incorrect.",
    passwordTooShort: "Password must be at least 8 characters.",
    usernameRequired: "Username is required.",
    usernameInvalid:
      "Username must be 1-96 characters of letters, digits, dot, dash or underscore, and start with a letter or digit.",
    usernameGitSuffix: "Username must not end in .git.",
    usernameReserved: "That username is reserved by this server. Pick another.",
  },
};
