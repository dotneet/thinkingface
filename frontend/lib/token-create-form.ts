/**
 * Create-form fields that must not survive a change of scope or expiry.
 *
 * Name stays: minting the same token under a different scope or lifetime
 * is the usual reason to switch. The error does not — a failed create is
 * a verdict about the scope and expiry that were selected when Create
 * ran. Same class as the webhook create form dropping its error when
 * repository scope changes.
 */
export function tokenCreateFormAfterContextSwitch(): { createError: null } {
  return { createError: null };
}
