/**
 * Create-form fields that must not survive a change of the administrator flag.
 *
 * Username, email and password stay: minting the same account as an
 * administrator (or not) is the usual reason to toggle. The error does
 * not — a failed create is a verdict about the role that was selected
 * when Submit ran. Same class as the token create form dropping its
 * error when scope or expiry changes.
 */
export function adminUserCreateFormAfterAdminToggle(): { error: null } {
  return { error: null };
}
