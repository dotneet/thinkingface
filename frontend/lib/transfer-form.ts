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

/**
 * Transfer-form fields that must not survive a change of destination
 * namespace.
 *
 * The optional new name stays: retargeting the same rename at another
 * namespace is the usual reason to switch. The error does not —
 * "namespace required" or a 404 about the previous destination is a
 * verdict about the namespace that was selected when Submit ran. Same
 * class as dropping the error when destination mode changes.
 */
export function transferFormAfterDestChange(): { submitError: null } {
  return { submitError: null };
}
