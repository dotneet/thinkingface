// Redirects for repositories that have been transferred or renamed
// (docs/dev/repo-transfer-design.md §9). resolveRepo is the one place that turns
// a miss in `repositories` into either a genuine 404 or a repoMovedError, and
// redirectMoved is the one place that turns a repoMovedError into the
// response shape a given route family expects.

package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// redirectMode selects how loadRepoForRead / loadRepoForWrite answer when the
// requested (kind, ns, name) turns out to be a former name of a repository
// that has since moved. It is a property of the route (chosen once per
// handler), never of the request.
type redirectMode int

const (
	// redirectHF answers 308 Permanent Redirect, preserving method and body:
	// huggingface_hub / requests follow 307/308 without downgrading POST, so
	// create_commit and upload_file keep working against an old repo id.
	redirectHF redirectMode = iota
	// redirectGit answers 301 Moved Permanently, which git's default
	// http.followRedirects=initial rebases subsequent requests on.
	redirectGit
	// redirectUI answers 404 with a repo_moved error body carrying the new
	// location, for the frontend to permanentRedirect() itself (CLAUDE.md
	// invariant 3: apiFetch never throws).
	redirectUI
	// redirectNone treats a moved repository exactly like one that never
	// existed: no Location, no repo_moved body. Used for
	// DELETE /api/repos/delete on an old name, which must not let a client
	// delete the repository sitting at the new name by accident
	// (docs/dev/repo-transfer-design.md §6).
	redirectNone
)

// repoMovedError is returned by resolveRepo when (kind, ns, name) is a former
// name of a repository that has since been transferred or renamed.
type repoMovedError struct {
	Namespace string
	Name      string
}

func (e *repoMovedError) Error() string {
	return "repository moved to " + e.Namespace + "/" + e.Name
}

// resolveRepo looks up (kind, ns, name), falling back to repo_redirects when
// the direct lookup misses so callers can tell "never existed" apart from
// "used to live here" (docs/dev/repo-transfer-design.md §9).
//
// kind == "" resolves either kind, like store.GetRepoAnyKind, but then
// cannot consult repo_redirects (its primary key includes kind), so a miss
// is always plain ErrNotFound.
func (s *Server) resolveRepo(ctx context.Context, kind, ns, name string) (*store.Repo, error) {
	var repo *store.Repo
	var err error
	if kind == "" {
		repo, err = s.store.GetRepoAnyKind(ctx, ns, name)
	} else {
		repo, err = s.store.GetRepo(ctx, kind, ns, name)
	}
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, store.ErrNotFound) || kind == "" {
		return nil, err
	}

	moved, rerr := s.store.ResolveRepoRedirect(ctx, kind, ns, name)
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, rerr
	}
	return nil, &repoMovedError{Namespace: moved.Namespace, Name: moved.Name}
}

// redirectMoved writes the response for a resolveRepo repoMovedError,
// according to mode. ns/name are the request's original (kind-stripped)
// segments, used to locate them in the request path for redirectHF/Git.
func redirectMoved(w http.ResponseWriter, r *http.Request, mode redirectMode, ns, name string, moved *repoMovedError) {
	switch mode {
	case redirectHF:
		http.Redirect(w, r, movedLocation(r, ns, name, moved), http.StatusPermanentRedirect)
	case redirectGit:
		http.Redirect(w, r, movedLocation(r, ns, name, moved), http.StatusMovedPermanently)
	default: // redirectUI
		message := "repository moved to " + moved.Namespace + "/" + moved.Name
		// Written by hand rather than through writeError, because the body
		// carries a field that helper knows nothing about (moved_to, which is
		// what the frontend redirects on). The header is set all the same:
		// "every error response carries X-Error-Message" is a contract-level
		// promise (docs/dev/api-contract.md §0), and this was the one UI error
		// quietly not keeping it.
		w.Header().Set("X-Error-Message", message)
		writeJSON(w, http.StatusNotFound, apitypes.ApiErrorBody{Error: apitypes.ApiError{
			Type:    "repo_moved",
			Message: message,
			MovedTo: &apitypes.RepoLocation{Namespace: moved.Namespace, Name: moved.Name},
		}})
	}
}

// movedLocation rewrites the request path's "/{ns}/{name}" segment to the
// repository's current name, preserving the rest of the path -- including a
// ".git" suffix git clients append to the name segment -- and the query
// string.
//
// It works on EscapedPath(), never on Path, and escapes the segment it splices
// in. Path is decoded, so building a Location out of it destroys exactly the
// two things this API cannot afford to lose:
//
//   - a "%2F" in the revision, which is how huggingface_hub sends every
//     revision (quote(rev, safe="")) and the only way a branch called
//     "feature/x" survives the URL at all. Decoded into a real "/", the
//     redirect points at revision "feature" and the follow-up is a 404
//     RevisionNotFound;
//   - a literal "%" in a file name ("a%b.txt", encoded as "a%25b.txt"), which
//     decodes to a Location that is not a valid URL. net/http refuses to
//     follow it with `invalid URL escape "%b."`.
//
// Both 308 (redirectHF) and 301 (redirectGit) go through here, so a renamed or
// transferred repository would otherwise be unreachable through its old name
// for precisely the clients this server exists to serve.
func movedLocation(r *http.Request, ns, name string, moved *repoMovedError) string {
	path := r.URL.EscapedPath()
	// Namespace and repository names are validated to characters that need no
	// escaping (names.go), so the escaped form of the old segment is the
	// segment itself; the new one is escaped anyway rather than trusting that
	// rule to hold forever.
	oldSeg := "/" + ns + "/" + name
	newSeg := "/" + url.PathEscape(moved.Namespace) + "/" + url.PathEscape(moved.Name)
	if idx := strings.Index(path, oldSeg); idx >= 0 {
		path = path[:idx] + newSeg + path[idx+len(oldSeg):]
	} else if oldSegGit := oldSeg + ".git"; strings.Contains(path, oldSegGit) {
		idx := strings.Index(path, oldSegGit)
		path = path[:idx] + newSeg + ".git" + path[idx+len(oldSegGit):]
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	return path
}
