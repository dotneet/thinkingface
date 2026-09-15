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

/**
 * URL to put in the webhook edit panel when it opens.
 *
 * Same lag as `active`: Save writes the new URL locally, but `webhook.url`
 * stays on the previous value until `onChanged()` refetches. Seeding from
 * the prop would put the old endpoint back in the field; Save then undoes
 * the write that just landed.
 */
export function seedWebhookEditUrl(committedUrl: string, _propUrl: string): string {
  return committedUrl;
}

/**
 * Whether the edit-panel URL is an unsaved change.
 *
 * Compare against the last committed write, not `webhook.url`. After Save
 * the prop still names the previous endpoint, so a just-saved URL looks
 * unsaved and Rotate warns about a change that already landed. "Reverting"
 * the field to match the stale prop + Save restores the old URL.
 */
export function webhookUrlIsUnsaved(localUrl: string, committedUrl: string): boolean {
  return localUrl !== committedUrl;
}

/**
 * Events to put in the webhook edit panel when it opens.
 *
 * Same lag as the URL: Save updates the subscription locally, but
 * `webhook.events` is the previous set until the parent refetch.
 */
export function seedWebhookEditEvents<T>(committedEvents: readonly T[], _propEvents: readonly T[]): T[] {
  return [...committedEvents];
}

/**
 * Whether the edit-panel event set is an unsaved change.
 *
 * Membership only — order is not part of the stored value. Compared to the
 * last committed write, not `webhook.events`, for the same refetch-lag
 * reason as `webhookUrlIsUnsaved`.
 */
export function webhookEventsAreUnsaved<T>(localEvents: ReadonlySet<T>, committedEvents: ReadonlySet<T>): boolean {
  return (
    localEvents.size !== committedEvents.size || Array.from(localEvents).some((e) => !committedEvents.has(e))
  );
}
