/**
 * Add-member fields that must not survive a change of username.
 *
 * Role stays: inviting the same person as a reader or an admin is the
 * usual reason to switch, and a "user not found" is a verdict about the
 * username, not the role. The error does not survive a username edit —
 * that 404 named the previous person. Same class as the admin user
 * create form dropping its error when the username changes.
 */
export function orgMemberAddFormAfterUsernameChange(): { addError: null } {
  return { addError: null };
}
