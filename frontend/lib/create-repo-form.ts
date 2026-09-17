/**
 * Create-form fields that must not survive a change of namespace or kind.
 *
 * Name and description stay: creating the same repository in another
 * namespace (or as the other kind) is the usual reason to switch. The
 * error does not — "already exists" / reserved_name / a 403 from A is a
 * verdict about A, and leftover 15 already drops webhook create errors
 * on a namespace switch for the same reason.
 */
export function createRepoFormAfterContextSwitch(): { error: null } {
  return { error: null };
}
