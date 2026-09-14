/**
 * `active` to put in the webhook edit panel when it opens.
 *
 * `committedActive` is the last value Enable/Disable or Save successfully
 * wrote. `propActive` is `webhook.active` and lags until the parent refetch.
 * Seeding from the prop is what used to re-enable a webhook you had just
 * disabled (or disable one you had just enabled) the moment Edit was opened.
 */
export function seedWebhookEditActive(committedActive: boolean, _propActive: boolean): boolean {
  return committedActive;
}

/**
 * Whether the edit-panel Active checkbox is an unsaved change.
 *
 * Compare against the last committed write, not `webhook.active`: that prop
 * lags until the parent refetch, so a just-clicked Enable/Disable looks like
 * an unsaved edit. The Rotate confirmation then warns about a change that
 * already landed, and "reverting" the checkbox to match the stale prop +
 * Save re-enables (or disables) the webhook the header action just wrote.
 */
export function webhookActiveIsUnsaved(localActive: boolean, committedActive: boolean): boolean {
  return localActive !== committedActive;
}
