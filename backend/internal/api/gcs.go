// Where a revision's bytes live in the bucket.
//
// Objects are content-addressed (lfs/{oid}, blobs/{sha}), so the bucket has
// no human-readable layout to browse: nothing in it is named after a
// namespace, a repository or a path. This endpoint is what puts the names
// back, on the destination side -- it lists every file of an indexed ref with
// the gs:// URI of its object, and hands out a ready-made shell script that
// copies them into $DEST/{path}.

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// handleRepoGCS answers GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}.
//
// The listing comes from repo_files, which the sync worker rebuilds for every
// pushed ref -- not from git -- so it reflects exactly what has been
// published to object storage. A ref git knows about but the worker has not
// indexed yet (or an empty repository) answers 200 with no files; only a
// revision that exists nowhere is a 404.
func (s *Server) handleRepoGCS(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"),
		repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	rev := chi.URLParam(r, "rev")

	rows, err := s.store.ListRepoFiles(r.Context(), repo.ID, rev)
	if err != nil {
		internalError(w, "list repository files", err)
		return
	}
	if len(rows) == 0 && !s.revKnownToGit(repo, rev) {
		notFound(w, "revision "+rev+" not found")
		return
	}

	files := make([]apitypes.RepoGCSFile, 0, len(rows))
	for _, f := range rows {
		files = append(files, apitypes.RepoGCSFile{
			Path: f.Path, Size: f.Size, LFS: f.LFSOID != nil, URI: s.fileURI(f),
		})
	}
	// ListRepoFiles orders by path already, but the collation behind that
	// ORDER BY differs between PostgreSQL and SQLite; the script and the
	// listing have to be byte-for-byte reproducible, so sort here too.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	writeJSON(w, http.StatusOK, apitypes.RepoGCSResponse{
		Ref:           rev,
		Files:         files,
		GcloudScript:  buildGcloudScript(repo.Kind, repo.Namespace, repo.Name, rev, files),
		DuckDBSnippet: buildDuckDBSnippet(files),
	})
}

// fileURI resolves one indexed file to the object holding its bytes: the
// deduplicated lfs/ object for an LFS file, the blobs/ object the sync worker
// published for any other file.
func (s *Server) fileURI(f store.RepoFile) string {
	if f.LFSOID != nil {
		return s.storage.PublicURI(storage.LFSKey(*f.LFSOID))
	}
	return s.storage.PublicURI(storage.BlobKey(f.BlobSHA))
}

// revKnownToGit reports whether rev names anything in the repository: a
// branch, a tag or a commit. It only runs when the file index came back
// empty, to tell "nothing published for this ref yet" (200, no files) apart
// from "no such revision" (404).
//
// An empty repository counts as known -- every revision of it is legitimately
// empty -- and so does a repository whose bare directory is not readable
// here, since that is a local condition and no evidence about the revision.
func (s *Server) revKnownToGit(repo *store.Repo, rev string) bool {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		return true
	}
	if _, err := gitRepo.Resolve(rev); err == nil {
		return true
	}
	// Resolve reports ErrEmptyRepo for any unresolvable name, so ask HEAD
	// directly rather than trusting that error to mean the repository is
	// empty.
	return gitRepo.IsEmpty()
}

// ------------------------------------------------------------------ snippets

// buildGcloudScript renders the POSIX shell script that reassembles a
// revision from content-addressed objects. Pure: everything it needs is in
// its arguments, so the exact bytes are testable without a server.
//
// DEST defaults to ./{name} and may be overridden with a local directory or a
// gs:// prefix; cp_one creates parent directories only in the local case,
// because gs:// has no directories to create.
func buildGcloudScript(kind, namespace, name, ref string, files []apitypes.RepoGCSFile) string {
	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# thinkingface: %s/%s/%s @ %s -- %d files, %d bytes\n",
		kindPlural(kind), namespace, name, ref, len(files), totalBytes)
	b.WriteString("# Objects are content-addressed; this script lays them out under DEST.\n")
	b.WriteString("# DEST may be a local directory or a gs:// prefix.\n")
	b.WriteString("set -eu\n")
	fmt.Fprintf(&b, "DEST=\"${DEST:-./%s}\"\n", name)
	b.WriteString("cp_one() {\n")
	b.WriteString("  case \"$DEST\" in gs://*) ;; *) mkdir -p \"$(dirname \"$2\")\" ;; esac\n")
	b.WriteString("  gcloud storage cp \"$1\" \"$2\"\n")
	b.WriteString("}\n")
	for _, f := range files {
		fmt.Fprintf(&b, "cp_one %s \"$DEST\"/%s\n", shellSingleQuote(f.URI), shellSingleQuote(f.Path))
	}
	return b.String()
}

// buildDuckDBSnippet renders a read_parquet() over the revision's parquet
// files, or "" when it has none.
func buildDuckDBSnippet(files []apitypes.RepoGCSFile) string {
	uris := make([]string, 0, len(files))
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".parquet") {
			uris = append(uris, f.URI)
		}
	}
	if len(uris) == 0 {
		return ""
	}

	quoted := make([]string, len(uris))
	for i, uri := range uris {
		quoted[i] = sqlSingleQuote(uri)
	}
	return "-- DuckDB: INSTALL httpfs; LOAD httpfs; then CREATE SECRET for GCS (HMAC) before running.\n" +
		"SELECT * FROM read_parquet([\n  " + strings.Join(quoted, ",\n  ") + "\n]);\n"
}

// gcloudCopyCommand is the one-file form of the script: what the file browser
// shows next to a download button. Same quoting rule, same single owner of it.
func gcloudCopyCommand(uri, dest string) string {
	return "gcloud storage cp " + shellSingleQuote(uri) + " " + shellSingleQuote(dest)
}

// shellSingleQuote wraps s in single quotes so the shell takes it literally.
// A single-quoted string cannot contain a single quote, so each one is
// replaced by the usual close-escape-reopen sequence instead.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sqlSingleQuote is the same idea for a SQL string literal, where an embedded
// quote is doubled.
func sqlSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
