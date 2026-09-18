/**
 * Transfer-form fields that must not survive a change of destination mode.
 *
 * Destination namespace and the optional new name stay: switching
 * "one of mine" / "someone else" is how you pick where the same
 * repository should go. The error does not — "namespace required" is a
 * verdict about the mode that was selected when Submit ran.
 */
export function transferFormAfterDestModeSwitch(): { submitError: null } {
  return { submitError: null };
}
