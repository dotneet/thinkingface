package syncer

import (
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/repocard"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// lineageEdges turns the raw references in a repository card into the rows
// repo_lineage stores.
//
// Normalisation is deliberately forgiving: a reference that does not parse is
// still kept, with empty target columns, so the UI can show what the card said
// and mark it as unresolved. Losing it silently would leave an author with a
// typo staring at a lineage section that claims there is no lineage.
//
// repoKind ("model" / "dataset") only decides which HuggingFace card fields
// are read as fallbacks -- a dataset card's `source_datasets:` is one, a model
// card's `model-index:` is not a dataset card field at all -- and never which
// edges are allowed. A dataset may declare a successor exactly like a model.
//
// files is the repository's file index at the revision being synced. It is
// only read to settle the base model relation (finetune / adapter / quantized
// / merge) for a card that does not declare one: the decision is made from
// path names alone, so no blob and no checkpoint header is fetched for it.
func lineageEdges(repoKind string, card repocard.Card, files []store.RepoFile) []store.LineageEdge {
	l := card.LineageFor(repoKind)
	out := make([]store.LineageEdge, 0,
		len(l.Datasets)+len(l.BaseModels)+len(l.EvalDatasets)+len(l.Runs)+1)

	relation := ""
	if len(l.BaseModels) > 0 {
		relation = repocard.ResolveBaseModelRelation(card, filePaths(files))
	}

	for i, raw := range l.Datasets {
		out = append(out, repoEdge(store.LineageKindDataset, raw, i, ""))
	}
	for i, raw := range l.BaseModels {
		out = append(out, repoEdge(store.LineageKindBaseModel, raw, i, relation))
	}
	for i, raw := range l.EvalDatasets {
		out = append(out, repoEdge(store.LineageKindEvalDataset, raw, i, ""))
	}
	for i, raw := range l.Runs {
		out = append(out, runEdge(raw, i))
	}
	if l.NewVersion != "" {
		// At most one: a repository has a single successor, and the chain walk
		// on the read side would have no way to choose between two.
		out = append(out, repoEdge(store.LineageKindNewVersion, l.NewVersion, 0, ""))
	}
	return out
}

// filePaths is the projection of the file index the relation inference needs.
func filePaths(files []store.RepoFile) []string {
	out := make([]string, len(files))
	for i := range files {
		out[i] = files[i].Path
	}
	return out
}

// repoEdge parses "namespace/name" with an optional "@revision" suffix, where
// the revision is a branch, a tag or a commit SHA. relation is carried on
// base_model edges only and is "" for everything else.
func repoEdge(kind, raw string, ordinal int, relation string) store.LineageEdge {
	e := store.LineageEdge{Kind: kind, Raw: strings.TrimSpace(raw), Relation: relation, Ordinal: ordinal}
	ref, rev := splitRev(e.Raw)
	parts := splitRef(ref)
	if len(parts) != 2 {
		return e
	}
	e.Namespace, e.Name, e.Rev = parts[0], parts[1], rev
	return e
}

// runEdge parses "namespace/repo/project/run": the experiment repository that
// holds the metrics, the project inside it, and the run itself.
func runEdge(raw string, ordinal int) store.LineageEdge {
	e := store.LineageEdge{Kind: store.LineageKindRun, Raw: strings.TrimSpace(raw), Ordinal: ordinal}
	// A run has no revision of its own; a trailing "@..." would be a mistake,
	// and dropping it here would resolve to the wrong run rather than to none.
	parts := splitRef(e.Raw)
	if len(parts) != 4 {
		return e
	}
	e.Namespace, e.Name, e.Project, e.Run = parts[0], parts[1], parts[2], parts[3]
	return e
}

// splitRev cuts a trailing "@revision" off a reference. The last "@" wins, so a
// revision may not contain one -- git refs cannot anyway.
func splitRev(raw string) (ref, rev string) {
	if i := strings.LastIndex(raw, "@"); i > 0 {
		return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
	}
	return raw, ""
}

// splitRef splits a slash-separated reference, tolerating the surrounding
// slashes a copied URL path leaves behind. It returns nil when any segment is
// blank, which is what makes "team//name" resolve to nothing instead of to a
// namespace with an empty name.
func splitRef(ref string) []string {
	ref = strings.Trim(strings.TrimSpace(ref), "/")
	if ref == "" {
		return nil
	}
	parts := strings.Split(ref, "/")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return nil
		}
	}
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
