/**
 * Create-form fields that must not survive a change of the organisation
 * name.
 *
 * Display name and description stay: filling those in after a "name
 * reserved" or "name invalid" is the usual next step, and those two
 * fields are not what the error named. The error does not survive a
 * name edit. Same class as the org member add form dropping its error
 * when the username changes.
 */
export function createOrgFormAfterNameChange(): { error: null } {
  return { error: null };
}
