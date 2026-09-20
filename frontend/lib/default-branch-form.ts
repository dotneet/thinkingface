/**
 * Form outcome that must not survive a change of the selected branch.
 *
 * The success banner is already dropped in the Select's onChange. The
 * error was not — a failed Save is a verdict about the branch that was
 * selected when Save ran. Same class as the token create form dropping
 * its error when scope or expiry changes.
 */
export function defaultBranchFormAfterSelectionChange(): { error: null } {
  return { error: null };
}
