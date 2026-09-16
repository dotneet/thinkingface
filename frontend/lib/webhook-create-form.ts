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
