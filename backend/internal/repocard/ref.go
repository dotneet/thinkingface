package repocard

import "strings"

// A repository card names other repositories as plain text -- `base_model:
// alice/bert@v2`, `datasets: [alice/corpus]`, `runs: [alice/exp/proj/run-1]`.
// That text is parsed twice on opposite sides of the same index: the syncer
// splits it to write repo_lineage rows, and the store splits the `?base_model=`
// / `?dataset=` filters to look those rows back up.
//
// The two used to be separate implementations that disagreed. One cut the
// revision at the *last* "@" and the other at the first, so `a/b@x@y` was
// indexed as `a/b@x` and searched for as `a/b`; one treated a leading "@" as
// a revision-only reference and the other reduced it to an empty namespace.
// A reference the writer resolved and the reader did not is a repository that
// silently drops out of its own lineage listing, which is why the spelling
// lives here, once, rather than in either caller.
//
// repocard is the shared home because both sides already depend on it: the
// syncer parses the card through this package, and the store's filters are
// about what a card said.

// SplitRepoRef parses a "namespace/name" reference with an optional trailing
// "@revision", where the revision is a branch, a tag or a commit SHA. ok is
// false when the reference does not name exactly one repository, and the
// caller decides what that means -- the syncer keeps the raw text as an
// unresolved edge, the filters refuse to match anything.
//
// The last "@" wins, so a revision may not contain one -- git refs cannot
// anyway. A leading "@" is not a separator: "@main" has no namespace/name in
// front of it, so it is not a reference at all.
//
// Surrounding whitespace and slashes are tolerated, which is what a
// hand-copied URL path leaves behind. A blank segment is not: "team//name"
// resolves to nothing rather than to a namespace with an empty name.
func SplitRepoRef(raw string) (ns, name, rev string, ok bool) {
	ref, rev := splitRev(raw)
	parts := splitSegments(ref)
	if len(parts) != 2 {
		return "", "", "", false
	}
	return parts[0], parts[1], rev, true
}

// SplitRunRef parses "namespace/name/project/run": the experiment repository
// that holds the metrics, the project inside it, and the run itself.
//
// Deliberately not SplitRepoRef's four-segment sibling: a run has no revision
// of its own, so a trailing "@..." is a mistake, and stripping it here would
// resolve to the wrong run rather than to none.
func SplitRunRef(raw string) (ns, name, project, run string, ok bool) {
	parts := splitSegments(raw)
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

// splitRev cuts a trailing "@revision" off a reference.
func splitRev(raw string) (ref, rev string) {
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndex(raw, "@"); i > 0 {
		return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
	}
	return raw, ""
}

// splitSegments splits a slash-separated reference, tolerating the
// surrounding slashes a copied URL path leaves behind. It returns nil when
// any segment is blank.
func splitSegments(ref string) []string {
	ref = strings.Trim(strings.TrimSpace(ref), "/")
	if ref == "" {
		return nil
	}
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		parts[i] = p
	}
	return parts
}
