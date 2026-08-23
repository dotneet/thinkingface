package store

import "testing"

func TestAggregateUsageByNamespace_SumsPerNamespace(t *testing.T) {
	repos := []RepoUsage{
		{RepoID: 1, Namespace: "acme", Name: "a", LFSSize: 100, NumFiles: 3},
		{RepoID: 2, Namespace: "acme", Name: "b", LFSSize: 50, NumFiles: 2},
		{RepoID: 3, Namespace: "other", Name: "c", LFSSize: 10, NumFiles: 1},
	}

	got := AggregateUsageByNamespace(repos)

	want := []NamespaceUsage{
		{Namespace: "acme", LFSSize: 150, NumFiles: 5, NumRepos: 2},
		{Namespace: "other", LFSSize: 10, NumFiles: 1, NumRepos: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("AggregateUsageByNamespace() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AggregateUsageByNamespace()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAggregateUsageByNamespace_PreservesFirstSeenOrder(t *testing.T) {
	repos := []RepoUsage{
		{Namespace: "z"},
		{Namespace: "a"},
		{Namespace: "z"},
	}
	got := AggregateUsageByNamespace(repos)
	if len(got) != 2 || got[0].Namespace != "z" || got[1].Namespace != "a" {
		t.Errorf("AggregateUsageByNamespace() order = %+v, want [z, a]", got)
	}
}

func TestAggregateUsageByNamespace_EmptyInput(t *testing.T) {
	if got := AggregateUsageByNamespace(nil); len(got) != 0 {
		t.Errorf("AggregateUsageByNamespace(nil) = %+v, want empty", got)
	}
}
