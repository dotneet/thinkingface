// Compatibility shims for huggingface_hub calls that no other file owns:
// front-matter validation, the Xet refusal, the access check and the tag
// catalogues.
//
// These are HF-compatible endpoints, so the external protocol -- not
// internal/apitypes -- is the contract. Every request and response shape here
// is hand-written to match what huggingface_hub actually sends and reads (see
// docs/dev/api-contract.md), and is deliberately outside `make gen-types`.

package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ---------------------------------------------- repository target resolution
//
// Every HuggingFace-compatible route names its repository the same way -- a
// plural {repoType} segment, {ns}, and a {name} that git clients may have
// suffixed with ".git" -- or, for the three that take it in the body,
// a "type" field and a name that is either "name" or "ns/name". The four
// helpers below are the only places that reading happens, so the mapping
// cannot drift between endpoints: the URL shape and the body shape are the
// external protocol (docs/dev/api-contract.md), not ours to vary per handler.

// loadHFRepoForRead resolves the {repoType}/{ns}/{name} of an HF-compatible
// read and enforces read access, returning the stored kind alongside the
// repository because several handlers echo it back.
func (s *Server) loadHFRepoForRead(w http.ResponseWriter, r *http.Request) (*store.Repo, string, bool) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"),
		repoName(chi.URLParam(r, "name")), redirectHF)
	return repo, kind, ok
}

// loadHFRepoForWrite is loadHFRepoForRead with the write gate: a write-scoped
// token, at least `write` in the namespace, and a repository that is not
// archived (see loadRepoForWrite).
func (s *Server) loadHFRepoForWrite(w http.ResponseWriter, r *http.Request) (*store.Repo, string, bool) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"),
		repoName(chi.URLParam(r, "name")), redirectHF)
	return repo, kind, ok
}

// hfRepoType maps the `type` field of an HF request body onto the stored
// kind. Absent means "model", which is huggingface_hub's own default
// (HfApi.create_repo's repo_type=None), and the plural it sends elsewhere is
// accepted for the same reason kindFromURL accepts it: "datasets" and
// "dataset" both reach this server in the wild.
func hfRepoType(raw string) string {
	if raw == "" {
		return "model"
	}
	return strings.TrimSuffix(raw, "s")
}

// hfRepoTarget resolves the (kind, namespace, name) an HF request body names.
//
// huggingface_hub sends the repository either as "name" with a separate
// "organization", or as "ns/name" with no organization at all -- and with
// neither, meaning the caller's own namespace. All three spellings are in
// current use, so all three are read here rather than in each handler.
func hfRepoTarget(user *store.User, rawType, rawName, org string) (kind, ns, name string) {
	kind = hfRepoType(rawType)
	ns, name = org, rawName
	if before, after, found := strings.Cut(rawName, "/"); found {
		ns, name = before, after
	}
	if ns == "" {
		ns = user.Username
	}
	return kind, ns, name
}

// handleValidateYAML checks a README's front matter before a commit. Only
// genuinely unparseable YAML is an error here: thinkingface does not enforce
// the HuggingFace card taxonomy, so a card with unfamiliar fields is fine.
//
// Authentication is required even though nothing here is repository-scoped:
// yaml.Unmarshal on a megabyte of deeply nested input is real CPU, and
// huggingface_hub only ever calls this from create_commit with the commit's
// own token (HfApi._validate_yaml), so requiring one costs no compatibility.
func (s *Server) handleValidateYAML(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUser(w, r); !ok {
		return
	}
	var req struct {
		Content  string `json:"content"`
		RepoType string `json:"repoType"`
	}
	if !decodeJSON(w, r, maxYAMLBody, &req, "request body must be JSON with a content field") {
		return
	}
	errs := []map[string]string{}
	if strings.HasPrefix(req.Content, "---") {
		var probe map[string]any
		front := req.Content
		if _, rest, found := strings.Cut(front, "---\n"); found {
			if body, _, ok := strings.Cut(rest, "\n---"); ok {
				front = body
			}
		}
		if err := yaml.Unmarshal([]byte(front), &probe); err != nil {
			errs = append(errs, map[string]string{"message": "invalid YAML front matter: " + err.Error()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"errors": errs, "warnings": []any{}})
}

// handleXetUnsupported explains the one client-side setting needed to talk to
// this server. Xet is a deliberate non-goal: LFS already moves the same bytes
// straight into GCS, and supporting a second content-addressed transfer
// protocol would double the storage surface for no gain here.
func (s *Server) handleXetUnsupported(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "xet_not_supported",
		"thinkingface transfers large files over Git LFS, not Xet. "+
			"Set HF_HUB_DISABLE_XET=1 in the environment (or call thinkingface.login(), which sets it for you) and retry.")
}

// handleHFAuthCheck answers GET /api/{type}s/{ns}/{name}/auth-check, which
// `huggingface_hub.utils.auth_check()` and `HfApi.auth_check()` call to find
// out whether they may read a repository before they start doing so.
//
// On the Hub the call separates three answers an ordinary read cannot: the
// repository is not there (RepositoryNotFoundError), it is gated and this
// caller has not been let in (GatedRepoError), or access is fine -- in which
// case the function returns None and nothing about the body matters.
//
// Only the first and the last exist here. This instance has no
// per-repository visibility and no gating at all: anyone who can reach it can
// read every repository on it, and the network boundary around the instance
// is the only read boundary there is (docs/dev/thinkingface-design.md §11).
// So "may this caller read it" collapses into "does it exist", which is
// exactly the question loadRepoForRead already answers -- including the
// X-Error-Code: RepoNotFound header huggingface_hub turns into
// RepositoryNotFoundError, and the redirect a renamed repository gets.
// Refusing an unauthenticated caller here would be a lie: the same caller can
// clone the repository a moment later.
//
// Until this route existed the call fell through to chi's own 404, which
// hf_raise_for_status reads as "no such repository" -- so a client asking
// about its access to a perfectly readable repository was told the repository
// was gone.
func (s *Server) handleHFAuthCheck(w http.ResponseWriter, r *http.Request) {
	repo, kind, ok := s.loadHFRepoForRead(w, r)
	if !ok {
		return
	}
	// huggingface_hub reads nothing but the status. The body names what was
	// checked because every response on this server is JSON, and a caller
	// driving the API by hand gets more from that than from an empty 200.
	writeJSON(w, http.StatusOK, map[string]any{
		"repoId":   repo.FullName(),
		"repoType": kind,
	})
}

// hfTag is one entry of a tags-by-type group. All three fields are read by
// huggingface_hub's older `ModelTags` / `DatasetTags` wrappers -- `label` and
// `id` are indexed directly, so neither may be missing -- while newer
// versions hand the decoded JSON back untouched.
type hfTag struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

// hfTagGroups is the set of group keys each catalogue must answer with.
//
// The keys are not ours to choose, and the list is not padding: huggingface_hub
// up to 0.17 unpacked the response through `GeneralTags`, which indexes a
// *fixed* list of keys and raises KeyError on a missing one. Every key it
// looks for is therefore always present, empty when this server has nothing
// to put in it -- an empty group is a truthful "no such tags here", while an
// absent group crashed the client outright.
//
// Only three of them can be populated from what this server indexes (see
// hfTagsByType); the rest are HuggingFace taxonomy dimensions that nothing in
// a repository card here is parsed into.
var hfTagGroups = map[string][]string{
	"model": {"library", "language", "license", "dataset", "pipeline_tag", "other"},
	"dataset": {
		"language", "multilinguality", "language_creators", "task_categories",
		"size_categories", "benchmark", "task_ids", "license", "other",
	},
}

// hfTaskGroup names the group a task facet belongs in, which differs between
// the two catalogues because the card field does: a model declares one
// `pipeline_tag`, a dataset a list of `task_categories`. store.taskFacet
// counts both under one facet, so the split is made here.
var hfTaskGroup = map[string]string{
	"model":   "pipeline_tag",
	"dataset": "task_categories",
}

func (s *Server) handleHFModelTags(w http.ResponseWriter, r *http.Request) {
	s.hfTagsByType(w, r, "model")
}

func (s *Server) handleHFDatasetTags(w http.ResponseWriter, r *http.Request) {
	s.hfTagsByType(w, r, "dataset")
}

// hfTagsByType answers GET /api/models-tags-by-type and
// /api/datasets-tags-by-type for `HfApi.get_model_tags()` /
// `get_dataset_tags()`.
//
// On the Hub this is a curated taxonomy. Here it is the same faceted
// aggregation the listing sidebar is built from, so the catalogue describes
// what is actually on this instance rather than what HuggingFace happens to
// know about -- which is the more useful answer for a self-hosted hub, and
// the only one it can give honestly. It is also why the counts are dropped:
// the HF shape has nowhere to put them.
//
// No authentication: it exposes nothing a repository listing does not, and
// huggingface_hub sends no token with the call.
func (s *Server) hfTagsByType(w http.ResponseWriter, r *http.Request, kind string) {
	// Limit 1 rather than 0: the facets are computed over everything matching
	// the filter regardless of the page window (store.repoFacets runs its own
	// queries), so the page itself is dead weight and is kept as small as the
	// listing query allows.
	_, _, facets, err := s.store.ListRepos(r.Context(), store.RepoFilter{
		Kind: kind, WithFacets: true, Limit: 1,
	})
	if err != nil {
		internalError(w, "list repository tags", err)
		return
	}

	out := make(map[string][]hfTag, len(hfTagGroups[kind]))
	for _, group := range hfTagGroups[kind] {
		// A non-nil empty slice: JSON's `[]` is what the client iterates,
		// while `null` is not iterable at all.
		out[group] = []hfTag{}
	}
	add := func(group string, items []store.RepoFacetItem) {
		for _, item := range items {
			if item.Value == "" {
				continue
			}
			// The value serves as both id and label: this server stores card
			// values verbatim and has no display names for them, and an empty
			// label is not an option (the client indexes it).
			out[group] = append(out[group], hfTag{ID: item.Value, Label: item.Value, Type: group})
		}
	}
	add("license", facets.Licenses)
	add(hfTaskGroup[kind], facets.Tasks)
	// Free-form card tags are exactly what "other" is on the Hub: everything
	// that is not one of the taxonomy dimensions above. base_model relations
	// (facets.Relations) are deliberately left out -- they describe an edge
	// between two repositories, not a tag anyone can filter a listing by.
	add("other", facets.Tags)

	writeJSON(w, http.StatusOK, out)
}
