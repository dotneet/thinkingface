// Storage usage: the settings dashboard that answers "how much GCS storage
// am I responsible for", broken down by namespace and by repository.

package api

import (
	"net/http"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}

	nsRows, err := s.store.NamespacesForUser(r.Context(), user.ID)
	if err != nil {
		internalError(w, "load namespaces", err)
		return
	}
	names := make([]string, 0, len(nsRows))
	for _, n := range nsRows {
		names = append(names, n.Name)
	}

	repoUsage, err := s.store.UsageByRepo(r.Context(), names)
	if err != nil {
		internalError(w, "load usage", err)
		return
	}

	// The ceiling belongs next to the number it caps: an upload refused with
	// 507 by the LFS batch API is otherwise a surprise on a page that only
	// ever said how much was used. Only the namespace's own override is read
	// here -- resolving it against the instance default is EffectiveQuota's
	// job, because a stored 0 is a real quota and a configured 0 is not.
	overrides, err := s.store.NamespaceQuotaOverrides(r.Context(), names)
	if err != nil {
		internalError(w, "load storage quotas", err)
		return
	}

	writeJSON(w, http.StatusOK, apitypes.UsageResponse{
		Namespaces: s.toUsageNamespaces(store.AggregateUsageByNamespace(repoUsage), overrides),
		Repos:      toUsageRepos(repoUsage),
	})
}

func (s *Server) toUsageNamespaces(rows []store.NamespaceUsage, overrides map[string]int64) []apitypes.UsageNamespace {
	out := make([]apitypes.UsageNamespace, 0, len(rows))
	for _, r := range rows {
		var override *int64
		if v, ok := overrides[r.Namespace]; ok {
			override = &v
		}
		out = append(out, apitypes.UsageNamespace{
			Namespace: r.Namespace, LFSSize: r.LFSSize, NumFiles: r.NumFiles, NumRepos: r.NumRepos,
			QuotaBytes: store.EffectiveQuota(override, s.cfg.DefaultStorageQuotaBytes),
		})
	}
	return out
}

func toUsageRepos(rows []store.RepoUsage) []apitypes.UsageRepo {
	out := make([]apitypes.UsageRepo, 0, len(rows))
	for _, r := range rows {
		out = append(out, apitypes.UsageRepo{
			Namespace: r.Namespace, Name: r.Name, Kind: apitypes.RepoKind(r.Kind),
			FullName: r.Namespace + "/" + r.Name,
			LFSSize:  r.LFSSize, NumFiles: r.NumFiles,
		})
	}
	return out
}
