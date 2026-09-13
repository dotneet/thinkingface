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
