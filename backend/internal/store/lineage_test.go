package store

import (
	"errors"
	"testing"
)

// The successor walk is a pure function of its lookup, so the graph here is a
// plain map: "who supersedes whom", by repository name inside one namespace.
func chainLookup(edges map[string]string, calls *int) NewVersionLookup {
	return func(from NewVersionRef) (NewVersionRef, bool, error) {
		if calls != nil {
			*calls++
		}
		to, ok := edges[from.Name]
		if !ok {
			return NewVersionRef{}, false, nil
		}
		return NewVersionRef{Namespace: from.Namespace, Name: to}, true, nil
	}
}

func ref(name string) NewVersionRef { return NewVersionRef{Namespace: "team", Name: name} }

func TestResolveNewVersionChainFollowsToTheEnd(t *testing.T) {
	chain, err := ResolveNewVersionChain(ref("v1"),
		chainLookup(map[string]string{"v1": "v2", "v2": "v3"}, nil))
	if err != nil {
		t.Fatalf("ResolveNewVersionChain: %v", err)
	}
	if chain.Direct != ref("v2") {
		t.Errorf("Direct = %+v, want v2", chain.Direct)
	}
	if chain.Latest != ref("v3") {
		t.Errorf("Latest = %+v, want v3", chain.Latest)
	}
	if chain.Hops != 2 || chain.Truncated {
		t.Errorf("Hops = %d, Truncated = %v, want 2, false", chain.Hops, chain.Truncated)
	}
}

func TestResolveNewVersionChainNoSuccessor(t *testing.T) {
	chain, err := ResolveNewVersionChain(ref("v1"), chainLookup(nil, nil))
	if err != nil {
		t.Fatalf("ResolveNewVersionChain: %v", err)
	}
	if chain.Hops != 0 || chain.Latest != (NewVersionRef{}) || chain.Truncated {
		t.Errorf("chain = %+v, want the zero chain", chain)
	}
}

// The successor lookup resolves the namespace case-insensitively, so a card
// that spells it differently still names the same repository. A self-reference
// written that way has to be recognised as one, not walked to the depth cap.
func TestResolveNewVersionChainSelfReferenceFoldsNamespaceCase(t *testing.T) {
	lookup := func(from NewVersionRef) (NewVersionRef, bool, error) {
		return NewVersionRef{Namespace: "TEAM", Name: from.Name}, true, nil
	}
	chain, err := ResolveNewVersionChain(ref("v1"), lookup)
	if err != nil {
		t.Fatalf("ResolveNewVersionChain: %v", err)
	}
	if chain != (NewVersionChain{}) {
		t.Errorf("chain = %+v, want the zero chain", chain)
	}
}

// A card pointing at its own repository declares nothing: there is no newer
// version to send anyone to, so the chain is empty rather than truncated.
func TestResolveNewVersionChainSelfReference(t *testing.T) {
	chain, err := ResolveNewVersionChain(ref("v1"), chainLookup(map[string]string{"v1": "v1"}, nil))
	if err != nil {
		t.Fatalf("ResolveNewVersionChain: %v", err)
	}
	if chain != (NewVersionChain{}) {
		t.Errorf("chain = %+v, want the zero chain", chain)
	}
}

// A cycle further along is a real successor declaration that happens not to
// terminate: the direct hop is shown, with the warning flag set.
func TestResolveNewVersionChainCycle(t *testing.T) {
	for name, edges := range map[string]map[string]string{
		"back to the origin": {"v1": "v2", "v2": "v3", "v3": "v1"},
		"loop further out":   {"v1": "v2", "v2": "v3", "v3": "v2"},
	} {
		t.Run(name, func(t *testing.T) {
			chain, err := ResolveNewVersionChain(ref("v1"), chainLookup(edges, nil))
			if err != nil {
				t.Fatalf("ResolveNewVersionChain: %v", err)
			}
			if !chain.Truncated {
				t.Errorf("chain = %+v, want Truncated", chain)
			}
			if chain.Latest != ref("v2") || chain.Direct != ref("v2") || chain.Hops != 1 {
				t.Errorf("chain = %+v, want the direct successor v2 only", chain)
			}
		})
	}
}

func TestResolveNewVersionChainDepthLimit(t *testing.T) {
	// A straight line longer than the cap: v0 -> v1 -> ... -> v20.
	edges := map[string]string{}
	for i := range 20 {
		edges["v"+string(rune('a'+i))] = "v" + string(rune('a'+i+1))
	}
	calls := 0
	chain, err := ResolveNewVersionChain(ref("va"), chainLookup(edges, &calls))
	if err != nil {
		t.Fatalf("ResolveNewVersionChain: %v", err)
	}
	if !chain.Truncated || chain.Hops != 1 || chain.Latest != chain.Direct {
		t.Errorf("chain = %+v, want the direct successor only, truncated", chain)
	}
	if calls > MaxNewVersionChainDepth+1 {
		t.Errorf("lookup called %d times, want at most %d", calls, MaxNewVersionChainDepth+1)
	}
}

// Exactly at the cap the chain still terminates, so nothing is truncated.
func TestResolveNewVersionChainAtTheLimit(t *testing.T) {
	edges := map[string]string{}
	for i := range MaxNewVersionChainDepth {
		edges["v"+string(rune('a'+i))] = "v" + string(rune('a'+i+1))
	}
	chain, err := ResolveNewVersionChain(ref("va"), chainLookup(edges, nil))
	if err != nil {
		t.Fatalf("ResolveNewVersionChain: %v", err)
	}
	if chain.Truncated || chain.Hops != MaxNewVersionChainDepth {
		t.Errorf("chain = %+v, want %d clean hops", chain, MaxNewVersionChainDepth)
	}
}

func TestResolveNewVersionChainPropagatesLookupError(t *testing.T) {
	boom := errors.New("boom")
	_, err := ResolveNewVersionChain(ref("v1"), func(NewVersionRef) (NewVersionRef, bool, error) {
		return NewVersionRef{}, false, boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

// The database half of the successor feature: one hop reads the index, honours
// the viewer's visibility, and refuses to cross repository kinds.
func TestIntegrationNewVersionEdges(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		v1 := f.repo(t, "alice", "m-v1", "model", nil)
		v2 := f.repo(t, "alice", "m-v2", "model", nil)
		f.repo(t, "alice", "m-v3", "model", nil)
		secret := f.repo(t, "bob", "m-secret", "model", nil)
		// A dataset sharing v2's name: a model's successor must not resolve to
		// it, in either direction.
		f.repo(t, "alice", "m-v2", "dataset", nil)
		gone := f.repo(t, "alice", "m-dangling", "model", nil)
		dsV1 := f.repo(t, "alice", "d-v1", "dataset", nil)
		f.repo(t, "alice", "d-v2", "dataset", nil)

		link := func(from *Repo, raw, ns, name string) {
			t.Helper()
			if err := s.ReplaceRepoLineage(ctx, from.ID, []LineageEdge{
				{Kind: LineageKindNewVersion, Raw: raw, Namespace: ns, Name: name},
			}); err != nil {
				t.Fatalf("ReplaceRepoLineage: %v", err)
			}
		}
		link(v1, "alice/m-v2", "alice", "m-v2")
		link(v2, "alice/m-v3", "alice", "m-v3")
		link(gone, "alice/nope", "alice", "nope")
		link(secret, "alice/m-v3", "alice", "m-v3")
		link(dsV1, "alice/d-v2", "alice", "d-v2")

		t.Run("chain walks to the newest version", func(t *testing.T) {
			chain, err := ResolveNewVersionChain(NewVersionRef{Namespace: "alice", Name: "m-v1"},
				s.NewVersionSuccessor(ctx, "model"))
			if err != nil {
				t.Fatalf("ResolveNewVersionChain: %v", err)
			}
			if chain.Hops != 2 || chain.Latest.Name != "m-v3" || chain.Direct.Name != "m-v2" {
				t.Errorf("chain = %+v", chain)
			}
		})

		t.Run("a dataset chain is walked on its own kind", func(t *testing.T) {
			chain, err := ResolveNewVersionChain(NewVersionRef{Namespace: "alice", Name: "d-v1"},
				s.NewVersionSuccessor(ctx, "dataset"))
			if err != nil || chain.Hops != 1 || chain.Latest.Name != "d-v2" {
				t.Fatalf("chain = %+v, err = %v", chain, err)
			}
		})

		t.Run("a successor that does not exist ends the chain", func(t *testing.T) {
			_, ok, err := s.NewVersionSuccessor(ctx, "model")(NewVersionRef{Namespace: "alice", Name: "m-dangling"})
			if ok || err != nil {
				t.Errorf("ok = %v, err = %v, want false, nil", ok, err)
			}
		})

		t.Run("kinds do not cross", func(t *testing.T) {
			// alice/m-v1 is a model; asking as a dataset finds nothing even
			// though a dataset named alice/m-v2 exists.
			_, ok, err := s.NewVersionSuccessor(ctx, "dataset")(NewVersionRef{Namespace: "alice", Name: "m-v1"})
			if ok || err != nil {
				t.Errorf("ok = %v, err = %v, want false, nil", ok, err)
			}
		})

		t.Run("predecessors are the reverse lookup", func(t *testing.T) {
			deps, err := s.ListNewVersionPredecessors(ctx, "model", "alice", "m-v3")
			if err != nil {
				t.Fatalf("ListNewVersionPredecessors: %v", err)
			}
			got := names(reposOf(deps))
			if !equalStrings(got, []string{"bob/m-secret", "alice/m-v2"}) &&
				!equalStrings(got, []string{"alice/m-v2", "bob/m-secret"}) {
				t.Errorf("predecessors = %v", got)
			}
			// And the same-named dataset never claims a model's predecessor.
			deps, _ = s.ListNewVersionPredecessors(ctx, "dataset", "alice", "m-v3")
			if len(deps) != 0 {
				t.Errorf("dataset predecessors = %v, want none", names(reposOf(deps)))
			}
		})

		t.Run("upstream reports the successor's existence", func(t *testing.T) {
			up, err := s.ListRepoLineage(ctx, secret.ID)
			if err != nil || len(up) != 1 {
				t.Fatalf("ListRepoLineage = %+v, %v", up, err)
			}
			if up[0].Kind != LineageKindNewVersion || !up[0].Exists {
				t.Errorf("successor edge = %+v", up[0])
			}
			if up, _ := s.ListRepoLineage(ctx, gone.ID); len(up) != 1 || up[0].Exists {
				t.Errorf("dangling successor = %+v", up)
			}
		})
	})
}

func reposOf(deps []LineageDependent) []Repo {
	out := make([]Repo, 0, len(deps))
	for i := range deps {
		out = append(out, deps[i].Repo)
	}
	return out
}

func TestLineageEdgeTargetKind(t *testing.T) {
	cases := []struct {
		kind, source, want string
	}{
		{LineageKindBaseModel, "model", "model"},
		{LineageKindDataset, "model", "dataset"},
		{LineageKindEvalDataset, "model", "dataset"},
		{LineageKindRun, "model", "dataset"},
		{LineageKindNewVersion, "model", "model"},
		{LineageKindNewVersion, "dataset", "dataset"},
	}
	for _, c := range cases {
		if got := (LineageEdge{Kind: c.kind}).TargetKind(c.source); got != c.want {
			t.Errorf("%s edge from a %s targets %q, want %q", c.kind, c.source, got, c.want)
		}
	}
}
