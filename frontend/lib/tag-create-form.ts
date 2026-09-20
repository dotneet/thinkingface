/**
 * Create-form fields that must not survive a change of the tagged revision.
 *
 * Name and message stay: tagging the same release at another revision is
 * the usual reason to switch. The error does not — a failed create is a
 * verdict about the revision that was selected when Create ran. Same
 * class as the token create form dropping its error when scope or expiry
 * changes.
 */
export function tagCreateFormAfterRevChange(): { createError: null } {
  return { createError: null };
}
