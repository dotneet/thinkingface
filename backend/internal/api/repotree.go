// File tree APIs: the HuggingFace repo-info, refs, tree and paths-info
// endpoints, and the UI's own directory listing with its preview hints.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/modelmeta"
	"github.com/dotneet/thinkingface/backend/internal/repocard"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

type hfSibling struct {
	RFilename string     `json:"rfilename"`
	Size      *int64     `json:"size,omitempty"`
	BlobID    string     `json:"blobId,omitempty"`
	LFS       *hfLFSInfo `json:"lfs,omitempty"`
}

type hfLFSInfo struct {
	OID         string `json:"oid"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	PointerSize int64  `json:"pointerSize"`
}

func (s *Server) handleHFRepoInfo(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	rev := chi.URLParam(r, "rev")
	if rev == "" {
		rev = repo.DefaultBranch
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	// Resolved before the tree read so an unknown revision is a 404 rather
	// than a repository that looks empty: HfApi.revision_exists is nothing
	// but this endpoint's status code.
	commit, empty, ok := s.revisionOrEmpty(w, gitRepo, repo, rev)
	if !ok {
		return
	}
	var entries []gitrepo.Entry
	if !empty {
		// The resolved hash, not rev: one resolution per request.
		entries, _, err = gitRepo.Tree(commit.String(), "", true)
		if err != nil {
			handleStoreError(w, "read tree", err)
			return
		}
	}

	siblings := make([]hfSibling, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		size := e.TargetSize()
		sib := hfSibling{RFilename: e.Path, Size: &size, BlobID: e.Hash.String()}
		if e.LFS != nil {
			sib.LFS = &hfLFSInfo{OID: e.LFS.OID, SHA256: e.LFS.OID, Size: e.LFS.Size, PointerSize: e.Size}
		}
		siblings = append(siblings, sib)
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i].RFilename < siblings[j].RFilename })

	sha := commit.String()
	if commit.IsZero() {
		sha = ""
	}
	resp := map[string]any{
		"_id":          strconv.FormatInt(repo.ID, 10),
		"id":           repo.FullName(),
		"author":       repo.Namespace,
		"sha":          sha,
		"lastModified": repo.UpdatedAt.UTC().Format(time.RFC3339),
		"createdAt":    repo.CreatedAt.UTC().Format(time.RFC3339),
		// huggingface_hub reads this off every model/dataset info response.
		// There is no visibility concept here, so it is always false.
		"private":   false,
		"disabled":  false,
		"gated":     false,
		"tags":      repo.Tags(),
		"downloads": repo.Downloads,
		"likes":     0,
		"cardData":  repo.Card,
		"siblings":  siblings,
	}
	if repo.Kind == "model" {
		resp["modelId"] = repo.FullName()
		resp["pipeline_tag"] = repo.Card["pipeline_tag"]
		resp["library_name"] = repo.Card["library_name"]
		resp["config"] = map[string]any{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHFRefs(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}

	type ref struct {
		Name         string `json:"name"`
		Ref          string `json:"ref"`
		TargetCommit string `json:"targetCommit"`
	}
	branches, tags := []ref{}, []ref{}
	if names, err := gitRepo.Branches(); err == nil {
		for _, n := range names {
			h, _ := gitRepo.RefTarget("refs/heads/" + n)
			branches = append(branches, ref{Name: n, Ref: "refs/heads/" + n, TargetCommit: h.String()})
		}
	}
	if names, err := gitRepo.Tags(); err == nil {
		for _, n := range names {
			h, _ := gitRepo.RefTarget("refs/tags/" + n)
			tags = append(tags, ref{Name: n, Ref: "refs/tags/" + n, TargetCommit: h.String()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches, "tags": tags, "converts": []any{}})
}

type hfTreeEntry struct {
	Type string     `json:"type"`
	OID  string     `json:"oid"`
	Size int64      `json:"size"`
	Path string     `json:"path"`
	LFS  *hfLFSInfo `json:"lfs,omitempty"`
}

func (s *Server) handleHFTree(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true" || r.URL.Query().Get("recursive") == "1"
	// An unknown revision is a RevisionNotFound 404, not an empty listing --
	// otherwise list_repo_files(revision="typo") answers [] and the typo is
	// never seen. A repository with no commits at all still answers 200 [].
	commit, empty, ok := s.revisionOrEmpty(w, gitRepo, repo, chi.URLParam(r, "rev"))
	if !ok {
		return
	}
	var entries []gitrepo.Entry
	if !empty {
		entries, _, err = gitRepo.Tree(commit.String(), wildcardPath(r), recursive)
		if err != nil {
			if errors.Is(err, gitrepo.ErrPathNotFound) {
				// huggingface_hub only treats a 404 as "this path does not
				// exist" (EntryNotFoundError) when this header is present;
				// without it HfFileSystem.glob -- and therefore
				// datasets.Dataset.push_to_hub, which globs data/* before
				// uploading -- fails on a repo that has no such directory yet.
				w.Header().Set("X-Error-Code", "EntryNotFound")
			}
			handleStoreError(w, "read tree", err)
			return
		}
	}

	out := make([]hfTreeEntry, 0, len(entries))
	for _, e := range entries {
		item := hfTreeEntry{Type: "file", OID: e.Hash.String(), Size: e.TargetSize(), Path: e.Path}
		if e.IsDir {
			item.Type = "directory"
			item.Size = 0
		}
		if e.LFS != nil {
			item.LFS = &hfLFSInfo{OID: e.LFS.OID, SHA256: e.LFS.OID, Size: e.LFS.Size, PointerSize: e.Size}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

const (
	// maxPathsInfoPaths bounds one paths-info batch. huggingface_hub's
	// get_paths_info is used for a handful of files at a time; nothing
	// legitimate comes near this.
	maxPathsInfoPaths = 1000
	// maxPathBytes bounds a single path. git itself will not store anything
	// close to this.
	maxPathBytes = 4096
)

// handleHFPathsInfo answers the batch metadata lookup snapshot_download uses to
// plan a download without walking the whole tree.
func (s *Server) handleHFPathsInfo(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	var req struct {
		Paths  []string `json:"paths"`
		Expand bool     `json:"expand"`
	}
	// Tolerant on purpose: huggingface_hub's get_paths_info posts a
	// form-encoded body (requests' `data=`, not `json=`), which will not
	// decode here and has always been treated as "no paths". Turning that
	// into a 400 would break the client, so only the size ceiling is
	// enforced as an error.
	body := http.MaxBytesReader(w, r.Body, maxBatchBody)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("request body must be at most %d bytes", maxBatchBody))
			return
		}
	}
	// Each path costs a commit resolution plus a tree walk, and on a public
	// repository this endpoint is reachable without authentication -- so the
	// element count, not just the byte count, has to be bounded.
	if len(req.Paths) > maxPathsInfoPaths {
		badRequest(w, fmt.Sprintf("paths may contain at most %d entries", maxPathsInfoPaths))
		return
	}
	for _, p := range req.Paths {
		if len(p) > maxPathBytes || strings.ContainsRune(p, 0) {
			badRequest(w, fmt.Sprintf("each path must be at most %d bytes and must not contain NUL", maxPathBytes))
			return
		}
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	// Resolved once, ahead of the loop: a path that is simply absent is
	// skipped below, so without this an unknown revision would be
	// indistinguishable from "none of these paths exist" and
	// snapshot_download(revision="typo") would happily write an empty
	// snapshot. A repository with no commits keeps its 200 [].
	commit, empty, ok := s.revisionOrEmpty(w, gitRepo, repo, chi.URLParam(r, "rev"))
	if !ok {
		return
	}
	// An empty repository has nothing to stat, so the loop is skipped
	// entirely rather than asked about the zero hash once per path.
	paths := req.Paths
	if empty {
		paths = nil
	}
	// The resolved hash, not rev: one resolution per request, so a push
	// landing mid-batch cannot split one response across two commits.
	rev := commit.String()

	out := []hfTreeEntry{}
	for _, p := range paths {
		e, _, err := gitRepo.Stat(rev, p)
		if err != nil {
			continue
		}
		item := hfTreeEntry{Type: "file", OID: e.Hash.String(), Size: e.TargetSize(), Path: e.Path}
		if e.IsDir {
			item.Type = "directory"
			item.Size = 0
		}
		if e.LFS != nil {
			item.LFS = &hfLFSInfo{OID: e.LFS.OID, SHA256: e.LFS.OID, Size: e.LFS.Size, PointerSize: e.Size}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// ------------------------------------------------------------------ UI tree

func previewKind(path string) apitypes.PreviewKind {
	lower := strings.ToLower(path)
	base := lower
	if i := strings.LastIndex(lower, "/"); i >= 0 {
		base = lower[i+1:]
	}
	switch {
	case strings.HasSuffix(lower, ".parquet"):
		return apitypes.PreviewKindParquet
	case modelmeta.FormatFor(lower) != "":
		// Checkpoints get their header read instead of a byte preview.
		return apitypes.PreviewKindModel
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"), strings.HasSuffix(lower, ".rst"):
		return apitypes.PreviewKindMarkdown
	case strings.HasSuffix(lower, ".png"), strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"),
		strings.HasSuffix(lower, ".gif"), strings.HasSuffix(lower, ".webp"), strings.HasSuffix(lower, ".svg"):
		return apitypes.PreviewKindImage
	case isTextPreviewName(base):
		return apitypes.PreviewKindText
	default:
		return apitypes.PreviewKindBinary
	}
}

func isTextPreviewName(base string) bool {
	switch base {
	case "license", "licence", "copying", "authors", "notice", "changelog",
		"contributing", "dockerfile", "makefile", "jenkinsfile", "gemfile",
		"procfile", "vagrantfile", "gitignore", ".gitignore", ".gitattributes",
		".dockerignore", ".editorconfig", ".env", ".env.example", ".env.local":
		return true
	}
	textExt := []string{
		".txt", ".json", ".jsonl", ".yaml", ".yml", ".csv", ".tsv",
		".py", ".sh", ".bash", ".zsh", ".fish", ".toml", ".cfg", ".ini",
		".gitattributes", ".go", ".rs", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".css", ".html", ".xml", ".sql", ".r", ".rb", ".java", ".kt", ".c", ".h",
		".cpp", ".hpp", ".cs", ".php", ".swift", ".scala", ".lua", ".pl",
		".ps1", ".bat", ".diff", ".patch", ".log", ".ipynb",
	}
	for _, ext := range textExt {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

func (s *Server) handleUITree(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	rev := chi.URLParam(r, "rev")
	dir := wildcardPath(r)

	entries, _, err := gitRepo.Tree(rev, dir, false)
	if err != nil {
		handleStoreError(w, "read tree", err)
		return
	}

	// A plain file only has a copy command to offer once the sync worker has
	// indexed its blob for rev -- the worker publishes to blobs/ before it
	// writes the row, so the index is the record of what is in the bucket,
	// the same one /gcs/{rev} reads. Loaded once per listing, not once per
	// entry; a failure degrades to no commands rather than a broken listing.
	indexedBlobs, err := s.store.ListIndexedBlobSHAs(r.Context(), repo.ID, rev)
	if err != nil {
		slog.Warn("list indexed blobs", "repo", repo.FullName(), "rev", rev, "error", err)
	}

	// Attribution is best-effort decoration: a failure here degrades to a
	// listing without commit info rather than a broken file browser.
	lastCommits, latest, lcErr := gitRepo.LastCommits(rev, dir)
	if lcErr != nil {
		slog.Warn("resolve last commits", "repo", repo.FullName(), "rev", rev, "dir", dir, "error", lcErr)
	}

	out := make([]apitypes.TreeEntryUI, 0, len(entries))
	for _, e := range entries {
		item := apitypes.TreeEntryUI{
			Type: apitypes.EntryTypeFile, Name: e.Name, Path: e.Path, Size: e.TargetSize(),
			LFS: e.LFS != nil, OID: e.Hash.String(),
			IsParquet: strings.HasSuffix(strings.ToLower(e.Path), ".parquet"),
			IsModel:   modelmeta.FormatFor(e.Path) != "",
			Preview:   previewKind(e.Path),
		}
		switch {
		case e.IsDir:
			// A directory is not an object; there is nothing to copy.
			item.Type, item.Preview, item.Size = apitypes.EntryTypeDirectory, apitypes.PreviewKindNone, 0
		case e.LFS != nil:
			// LFS content is content-addressed and repository-independent, so
			// it is there no matter how the revision was named.
			item.GcloudCommand = gcloudCopyCommand(s.storage.PublicURI(storage.LFSKey(e.LFS.OID)), "./"+e.Name)
		case indexedBlobs[e.Hash.String()]:
			item.GcloudCommand = gcloudCopyCommand(s.storage.PublicURI(storage.BlobKey(e.Hash.String())), "./"+e.Name)
		}
		if meta, ok := lastCommits[e.Name]; ok {
			info := commitInfoUI(meta)
			item.LastCommit = &info
		}
		out = append(out, item)
	}
	// Directories first, then files, each alphabetically.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Type == apitypes.EntryTypeDirectory) != (out[j].Type == apitypes.EntryTypeDirectory) {
			return out[i].Type == apitypes.EntryTypeDirectory
		}
		return out[i].Name < out[j].Name
	})

	var readme *string
	var readmeTooLarge bool
	readmePath := "README.md"
	if dir != "" {
		readmePath = dir + "/README.md"
	}
	if content, err := gitRepo.ReadFile(rev, readmePath, maxReadmeBytes); err == nil {
		body := repocard.Parse(content).Body
		readme = &body
	} else if errors.Is(err, gitrepo.ErrBlobTooLarge) {
		readmeTooLarge = true
	}

	resp := apitypes.TreeResponseUI{Path: dir, Entries: out, Readme: readme, ReadmeTooLarge: readmeTooLarge}
	if latest != nil {
		info := commitInfoUI(*latest)
		resp.LatestCommit = &info
	}
	writeJSON(w, http.StatusOK, resp)
}

func commitInfoUI(m gitrepo.CommitMeta) apitypes.CommitInfoUI {
	return apitypes.CommitInfoUI{
		OID:     m.Hash.String(),
		Message: m.Message,
		Author:  m.Author,
		Date:    m.When.UTC(),
	}
}

// handleUIRefs feeds the file browser's revision picker.
func (s *Server) handleUIRefs(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}

	resp := apitypes.RefsResponseUI{
		Branches: []apitypes.RefUI{}, Tags: []apitypes.RefUI{},
		DefaultBranch: repo.DefaultBranch,
	}
	if names, err := gitRepo.Branches(); err == nil {
		for _, n := range names {
			h, _ := gitRepo.RefTarget("refs/heads/" + n)
			resp.Branches = append(resp.Branches, apitypes.RefUI{Name: n, TargetOID: h.String()})
		}
	}
	if names, err := gitRepo.Tags(); err == nil {
		for _, n := range names {
			h, _ := gitRepo.RefTarget("refs/tags/" + n)
			resp.Tags = append(resp.Tags, apitypes.RefUI{Name: n, TargetOID: h.String()})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

const (
	defaultCommitPage = 50
	maxCommitPage     = 100
)

// handleUICommits pages through a revision's first-parent history.
func (s *Server) handleUICommits(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}

	limit := defaultCommitPage
	if v := r.URL.Query().Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 {
			badRequest(w, "limit must be a positive integer")
			return
		}
		limit = min(n, maxCommitPage)
	}
	after := plumbing.ZeroHash
	if v := r.URL.Query().Get("after"); v != "" {
		// The cursor names an object, so it has to be a full hex hash; a
		// branch name here would silently restart the walk.
		parsed := plumbing.NewHash(v)
		if parsed.IsZero() || parsed.String() != strings.ToLower(v) {
			badRequest(w, "after must be a full commit hash")
			return
		}
		after = parsed
	}

	metas, next, err := gitRepo.ListCommits(chi.URLParam(r, "rev"), r.URL.Query().Get("path"), after, limit)
	if err != nil {
		handleStoreError(w, "list commits", err)
		return
	}
	resp := apitypes.CommitListResponse{Commits: make([]apitypes.CommitInfoUI, 0, len(metas))}
	for _, m := range metas {
		resp.Commits = append(resp.Commits, commitInfoUI(m))
	}
	if !next.IsZero() {
		cursor := next.String()
		resp.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, resp)
}
