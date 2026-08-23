// Lineage endpoints: where a repository came from (the edges its own card
// declares) and what came out of it (the reverse lookup over every other
// card). The index behind them is written by internal/syncer on every
// default-branch push.

package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func toLineageRef(u store.LineageUpstream, sourceKind string) apitypes.LineageRef {
	ref := apitypes.LineageRef{
		Kind:       apitypes.LineageEdgeKind(u.Kind),
		Raw:        u.Raw,
		TargetKind: apitypes.RepoKind(u.TargetKind(sourceKind)),
		Namespace:  u.Namespace,
		Name:       u.Name,
		Rev:        u.Rev,
		Project:    u.Project,
		Run:        u.Run,
		Relation:   u.Relation,
		Exists:     u.Exists,
	}
	if u.Namespace != "" && u.Name != "" {
		ref.FullName = u.Namespace + "/" + u.Name
	}
	return ref
}

func toLineageDependents(rows []store.LineageDependent) []apitypes.LineageDependent {
	out := make([]apitypes.LineageDependent, 0, len(rows))
	for i := range rows {
		d := &rows[i]
		out = append(out, apitypes.LineageDependent{
			Repo:     toSummary(&d.Repo),
			Kind:     apitypes.LineageEdgeKind(d.Edge.Kind),
			Raw:      d.Edge.Raw,
			Rev:      d.Edge.Rev,
			Project:  d.Edge.Project,
			Run:      d.Edge.Run,
			Relation: d.Edge.Relation,
		})
	}
	return out
}

// dependentKinds are the edge kinds that can point at a repository of this
// kind. A dataset is referenced directly, as an evaluation set, and -- when it
// holds experiment logs -- through the runs inside it.
//
// new_version is not here: it targets a repository of its own kind, so the
// reverse lookup has to filter on that and goes through
// ListNewVersionPredecessors instead.
func dependentKinds(repoKind string) []string {
	if repoKind == "model" {
		return []string{store.LineageKindBaseModel}
	}
	return []string{store.LineageKindDataset, store.LineageKindEvalDataset, store.LineageKindRun}
}

func (s *Server) handleRepoLineage(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	ctx := r.Context()

	upstream, err := s.store.ListRepoLineage(ctx, repo.ID)
	if err != nil {
		internalError(w, "list repository lineage", err)
		return
	}
	downstream, err := s.store.ListLineageDependents(ctx, dependentKinds(repo.Kind), repo.Namespace, repo.Name)
	if err != nil {
		internalError(w, "list lineage dependents", err)
		return
	}
	// "This is the successor of X" is the reverse of the new_version edge, and
	// belongs in the same downstream list as everything else pointing here.
	superseded, err := s.store.ListNewVersionPredecessors(ctx, repo.Kind, repo.Namespace, repo.Name)
	if err != nil {
		internalError(w, "list superseded repositories", err)
		return
	}

	// "A run says it produced this" is the other half of a model's provenance,
	// and it is not in repo_lineage: that index is rebuilt from the card on
	// every push, so a run's declaration is read from the run side instead
	// (store/experiments.go). Only a model can be the output of a run.
	producedBy := []apitypes.ExpRunProducer{}
	if repo.Kind == "model" {
		rows, err := s.store.ListModelProducers(ctx, repo.Namespace, repo.Name)
		if err != nil {
			internalError(w, "list model producers", err)
			return
		}
		for i := range rows {
			producedBy = append(producedBy, apitypes.ExpRunProducer{
				Repo:     toSummary(&rows[i].Repo),
				Project:  rows[i].Project,
				Run:      rows[i].Run,
				Revision: rows[i].Revision,
			})
		}
	}

	refs := make([]apitypes.LineageRef, 0, len(upstream))
	var successor *apitypes.LineageSuccessor
	for _, u := range upstream {
		if u.Kind == store.LineageKindNewVersion {
			// Reported as its own resolved chain rather than as an upstream
			// reference: it points forward in time, not back at an origin.
			successor, err = s.resolveSuccessor(ctx, repo.Kind, repo.Namespace, repo.Name, u)
			if err != nil {
				internalError(w, "resolve new version chain", err)
				return
			}
			continue
		}
		refs = append(refs, toLineageRef(u, repo.Kind))
	}
	writeJSON(w, http.StatusOK, apitypes.RepoLineageResponse{
		Upstream:   refs,
		Downstream: append(toLineageDependents(downstream), toLineageDependents(superseded)...),
		NewVersion: successor,
		ProducedBy: producedBy,
	})
}

// resolveSuccessor turns the repository's own `new_version:` edge into the
// answer a reader wants: not "there is a v2" but "v4 is the newest one".
//
// A declared successor that does not resolve stops right there (Hops 0): the
// reference is still worth showing -- it may be a typo, or simply unpushed --
// but nothing can be followed through it.
func (s *Server) resolveSuccessor(ctx context.Context, kind, ns, name string,
	edge store.LineageUpstream,
) (*apitypes.LineageSuccessor, error) {
	direct := toLineageRef(edge, kind)
	out := &apitypes.LineageSuccessor{Direct: direct, Latest: direct}
	if !direct.Exists {
		return out, nil
	}
	chain, err := store.ResolveNewVersionChain(
		store.NewVersionRef{Namespace: ns, Name: name},
		s.store.NewVersionSuccessor(ctx, kind))
	if err != nil {
		return nil, err
	}
	if chain.Hops == 0 {
		// The card points at this very repository: a self-reference declares
		// no successor at all, so there is nothing to send the reader to.
		return nil, nil
	}
	out.Hops = chain.Hops
	out.Truncated = chain.Truncated
	if chain.Hops == 1 {
		// One hop: the direct edge already says it, with the revision the card
		// pinned and the raw string it wrote.
		return out, nil
	}
	out.Latest = apitypes.LineageRef{
		Kind:       apitypes.LineageEdgeKind(store.LineageKindNewVersion),
		Raw:        chain.Latest.Namespace + "/" + chain.Latest.Name,
		TargetKind: apitypes.RepoKind(kind),
		Namespace:  chain.Latest.Namespace,
		Name:       chain.Latest.Name,
		FullName:   chain.Latest.Namespace + "/" + chain.Latest.Name,
		Exists:     true,
	}
	return out, nil
}

// handleExperimentLineage answers "what came out of this run?". Without a
// `run` query parameter it covers every run of the project in one round trip,
// which is what the run table needs to show a link per row.
func (s *Server) handleExperimentLineage(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	run := r.URL.Query().Get("run")
	rows, err := s.store.ListRunDependents(r.Context(), repo.Namespace, repo.Name,
		chi.URLParam(r, "project"), run)
	if err != nil {
		internalError(w, "list run lineage", err)
		return
	}

	// Group by run, preserving the order the rows came back in (most recently
	// updated dependent first) so the first run listed is the liveliest one.
	items := []apitypes.ExpRunLineage{}
	index := map[string]int{}
	if run != "" {
		// An explicit run always gets an entry, even an empty one: "this run
		// produced nothing yet" is an answer, not a missing resource.
		items = append(items, apitypes.ExpRunLineage{Run: run, Models: []apitypes.LineageDependent{}})
		index[run] = 0
	}
	for i := range rows {
		name := rows[i].Edge.Run
		at, ok := index[name]
		if !ok {
			at = len(items)
			index[name] = at
			items = append(items, apitypes.ExpRunLineage{Run: name, Models: []apitypes.LineageDependent{}})
		}
		items[at].Models = append(items[at].Models, toLineageDependents(rows[i:i+1])...)
	}
	writeJSON(w, http.StatusOK, apitypes.ExpLineageResponse{Items: items})
}
