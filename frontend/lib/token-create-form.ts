/**
 * Create-form fields that must not survive a change of scope or expiry.
 *
 * Name stays: minting the same token under a different scope or lifetime
 * is the usual reason to switch. The error does not — a failed create is
 * a verdict about the scope and expiry that were selected when Create
 * ran. Same class as the webhook create form dropping its error when
 * repository scope changes.
 */
export function tokenCreateFormAfterContextSwitch(): { createError: null } {
  return { createError: null };
}

/**
 * The create form's "restrict to repositories" textarea, one
 * `datasets/ns/name` or `models/ns/name` per line, as the list the API takes.
 * Blank lines and surrounding whitespace are dropped and repeats collapse;
 * the spelling itself is left for the server to judge, so there is one set of
 * rules and one error message for a malformed entry.
 */
export function parseTokenRepos(text: string): string[] {
  const out: string[] = [];
  for (const line of text.split("\n")) {
    const entry = line.trim();
    if (entry !== "" && !out.includes(entry)) out.push(entry);
  }
  return out;
}
