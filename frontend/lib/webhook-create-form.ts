/**
 * Create-form fields that must not survive a namespace switch.
 *
 * `repoScope` is already dropped in the namespace effect: a `model/foo`
 * selected under A would 400 under B once the option list is rebuilt. URL
 * and events used to stay — the form looked ready to submit against B with
 * A's endpoint and event set.
 */
export function webhookCreateFormAfterNamespaceSwitch<T = never>(): {
  repoScope: string;
  url: string;
  events: T[];
} {
  return { repoScope: "", url: "", events: [] };
}

/**
 * Create-form fields that must not survive a change of repository scope.
 *
 * URL and events stay: retargeting the same endpoint at another repo (or
 * at every repo) is the usual reason to switch. The error does not — a
 * 400 about the previous scope, or "select at least one event" from a
 * submit against that scope, is a verdict about the attempt that ran.
 */
export function webhookCreateFormAfterRepoScopeChange(): { createError: null } {
  return { createError: null };
}

/**
 * Create-form fields that must not survive a change of the event set.
 *
 * URL and repository scope stay: adding or dropping an event on the same
 * endpoint is the usual reason to toggle. The error does not — "select at
 * least one event" is a verdict about the set that was selected when
 * Create ran, and a 400 about that set is the same. Same class as
 * dropping the error when repository scope changes.
 */
export function webhookCreateFormAfterEventsChange(): { createError: null } {
  return { createError: null };
}
