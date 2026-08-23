// Compatibility shims for huggingface_hub calls that are not tied to a
// single repository: front-matter validation and the Xet refusal.

package api

import (
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
)

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
