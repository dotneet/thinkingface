/**
 * Create-form fields that must not survive a change of title or key.
 *
 * There is no context selector: title and key are the whole form. The
 * error does not survive an edit — "this key is already registered" or
 * "RSA keys must be at least 2048 bits" named the previous paste. Same
 * class as the org member add form dropping its error when the username
 * changes.
 */
export function sshKeyCreateFormAfterFieldChange(): { addError: null } {
  return { addError: null };
}
