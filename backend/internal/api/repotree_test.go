// Tests for the two halves of repotree.go that a real client actually hits:
// the paths-info batch, whose body huggingface_hub sends form-encoded rather
// than as JSON, and the Web UI's own tree / commits listings, which used to
// answer an unknown revision with an empty page instead of a 404.
//
// The fixture is revision_test.go's -- same package, same real Server over
// real HTTP -- so the "empty repository" and "unknown revision" repositories
// are built exactly once.

package api

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// doForm posts a body the way `requests` does for `data={...}`, which is how
// huggingface_hub's get_paths_info sends its batch.
func (f *revisionFixture) doForm(path string, form url.Values) response {
	f.t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return response{rec: rec}
}

// doRawBody posts a body verbatim under a content type of the caller's
// choosing, for the size ceiling -- url.Values.Encode would have to build the
// oversized string anyway.
func (f *revisionFixture) doRawBody(path, contentType string, body []byte) response {
	f.t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return response{rec: rec}
}

type pathsInfoEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// ----------------------------------------------------------- paths-info body

// The regression this file exists for: huggingface_hub posts get_paths_info's
// batch as `data={"paths": [...], "expand": ...}`, which `requests` sends
// form-encoded. A JSON-only decoder read no paths from any real client, so the
// endpoint answered 200 [] to every one of them -- which HfFileSystem.info /
// .exists turn into a FileNotFoundError, and CommitOperationCopy into a
// failure to resolve its source.
func TestHFPathsInfo_FormEncodedBodyIsUnderstood(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	resp := f.doForm("/api/models/alice/foo/paths-info/main", url.Values{
		// "True" is what requests writes for a Python bool.
		"expand": {"True"},
		"paths":  {"README.md"},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var entries []pathsInfoEntry
	resp.json(t, &entries)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly one for README.md", entries)
	}
	if entries[0].Path != "README.md" || entries[0].Type != "file" {
		t.Fatalf("entry = %+v, want the README.md file", entries[0])
	}
	if entries[0].OID == "" || entries[0].Size == 0 {
		t.Fatalf("entry = %+v, want a real oid and size", entries[0])
	}
}

// Repeated keys are how a list travels in a form body, and paths that are not
// in the tree are skipped rather than reported -- the same rule the JSON
// branch has always followed.
func TestHFPathsInfo_FormEncodedListsAndMissingPaths(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	resp := f.doForm("/api/models/alice/foo/paths-info/main", url.Values{
		"paths": {"README.md", "no-such-file.txt"},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var entries []pathsInfoEntry
	resp.json(t, &entries)
	if len(entries) != 1 || entries[0].Path != "README.md" {
		t.Fatalf("entries = %+v, want just README.md", entries)
	}
}

// The JSON encoding keeps working: the Web UI and this repository's own
// contract examples use it, and the fix must not trade one caller for another.
func TestHFPathsInfo_JSONBodyStillReturnsEntries(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	resp := f.do("POST", "/api/models/alice/foo/paths-info/main", "",
		map[string]any{"paths": []string{"README.md"}, "expand": false})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var entries []pathsInfoEntry
	resp.json(t, &entries)
	if len(entries) != 1 || entries[0].Path != "README.md" {
		t.Fatalf("entries = %+v, want just README.md", entries)
	}
}

// A form body carries paths now, so the element count and per-path ceilings
// have to bind on that branch too -- they are the only thing standing between
// an unauthenticated caller and one tree walk per element.
func TestHFPathsInfo_FormEncodedRespectsTheLimits(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	t.Run("too many paths", func(t *testing.T) {
		form := url.Values{}
		for i := 0; i <= maxPathsInfoPaths; i++ {
			form.Add("paths", "README.md")
		}
		resp := f.doForm("/api/models/alice/foo/paths-info/main", form)
		if resp.status() != 400 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
	})

	t.Run("a path with NUL in it", func(t *testing.T) {
		resp := f.doForm("/api/models/alice/foo/paths-info/main", url.Values{
			"paths": {"READ\x00ME.md"},
		})
		if resp.status() != 400 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
	})

	t.Run("an oversized body", func(t *testing.T) {
		body := []byte("paths=" + strings.Repeat("a", maxBatchBody))
		resp := f.doRawBody("/api/models/alice/foo/paths-info/main",
			"application/x-www-form-urlencoded", body)
		if resp.status() != 413 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
	})
}

// -------------------------------------------------------- recursive=<bool>

// huggingface_hub's paginate() passes a Python bool straight into the query
// string, and its two HTTP backends spell one differently: 0.x (requests)
// title-cases it ("True"/"False"), 1.x (httpx) lowercases it
// ("true"/"false") -- both observed in the e2e venv. A case-sensitive
// "true"/"1" check missed every 0.x caller silently: list_repo_files() /
// list_repo_tree(recursive=True) / HfFileSystem's recursive listing dropped
// nested files without ever raising, which is worse than an error because
// nothing in a caller's own code says anything went wrong.
func TestHFTree_RecursiveAcceptsEveryPythonBoolSpelling(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.repo("alice", "foo")
	f.commitOps(repo, "main", "Add a nested file",
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "sub/nested.txt", Data: []byte("nested")})

	for _, tc := range []struct {
		recursive string
		wantDeep  bool
	}{
		{"True", true},   // requests' encoding of Python's True (huggingface_hub 0.x)
		{"true", true},   // httpx's encoding of Python's True (huggingface_hub 1.x)
		{"1", true},      // queryFlag's other truthy spelling, kept working
		{"False", false}, // requests' encoding of Python's False (huggingface_hub 0.x)
		{"false", false}, // httpx's encoding of Python's False (huggingface_hub 1.x)
		{"0", false},
		{"", false}, // omitted entirely: the default is non-recursive
	} {
		t.Run("recursive="+tc.recursive, func(t *testing.T) {
			path := "/api/models/alice/foo/tree/main"
			if tc.recursive != "" {
				path += "?recursive=" + tc.recursive
			}
			resp := f.do("GET", path, "", nil)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			var entries []hfTreeEntry
			resp.json(t, &entries)

			var paths []string
			for _, e := range entries {
				paths = append(paths, e.Path)
			}
			gotDeep := false
			for _, p := range paths {
				if p == "sub/nested.txt" {
					gotDeep = true
				}
			}
			if gotDeep != tc.wantDeep {
				t.Fatalf("recursive=%q: nested file present = %v, want %v (entries = %+v)",
					tc.recursive, gotDeep, tc.wantDeep, paths)
			}
			// The directory entry itself is listed either way -- only whether
			// its *contents* are inlined changes with recursive.
			gotDir := false
			for _, p := range paths {
				if p == "sub" {
					gotDir = true
				}
			}
			if !gotDir {
				t.Fatalf("recursive=%q: entries = %+v, want the \"sub\" directory entry regardless", tc.recursive, paths)
			}
		})
	}
}

// The revision checks sit in front of the decoder, so they answer the same way
// whichever encoding the batch arrived in.
func TestHFPathsInfo_FormEncodedRevisionHandling(t *testing.T) {
	t.Run("unknown revision", func(t *testing.T) {
		f := newRevisionFixture(t)
		f.repo("alice", "foo")

		resp := f.doForm("/api/models/alice/foo/paths-info/no-such-rev", url.Values{
			"paths": {"README.md"},
		})
		if resp.status() != 404 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
			t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
		}
	})

	t.Run("empty repository", func(t *testing.T) {
		f := newRevisionFixture(t)
		f.emptyRepo("alice", "foo")

		resp := f.doForm("/api/models/alice/foo/paths-info/main", url.Values{
			"paths": {"README.md"},
		})
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var entries []pathsInfoEntry
		resp.json(t, &entries)
		if len(entries) != 0 {
			t.Fatalf("entries = %+v, want none", entries)
		}
	})
}

// ------------------------------------------------------- UI tree and commits

// uiReadsOf names the two UI listings for one revision. Both used to answer an
// unknown revision with a 200 and nothing in it, which the file browser drew
// as "this repository is empty".
func uiReadsOf(rev string) []struct{ name, path string } {
	base := "/api/v1/repos/model/alice/foo"
	return []struct{ name, path string }{
		{"tree", base + "/tree/" + rev},
		{"commits", base + "/commits/" + rev},
	}
}

func TestUIListings_UnknownRevisionIsRevisionNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	for _, tc := range uiReadsOf("no-such-branch") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("GET", tc.path, "", nil)
			if resp.status() != 404 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
				t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
			}
		})
	}
}

// The half that must not change: a repository with no commits keeps its 200
// and its empty lists. The front end's empty state is built on exactly this,
// and it is what a freshly created repository looks like.
func TestUIListings_EmptyRepositoryStillAnswers200(t *testing.T) {
	f := newRevisionFixture(t)
	f.emptyRepo("alice", "foo")

	t.Run("tree", func(t *testing.T) {
		resp := f.do("GET", "/api/v1/repos/model/alice/foo/tree/main", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var body struct {
			Entries []struct {
				Name string `json:"name"`
			} `json:"entries"`
			Readme *string `json:"readme"`
		}
		resp.json(t, &body)
		if len(body.Entries) != 0 || body.Readme != nil {
			t.Fatalf("body = %+v, want no entries and no readme", body)
		}
	})

	t.Run("commits", func(t *testing.T) {
		resp := f.do("GET", "/api/v1/repos/model/alice/foo/commits/main", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var body struct {
			Commits []struct {
				OID string `json:"oid"`
			} `json:"commits"`
		}
		resp.json(t, &body)
		if len(body.Commits) != 0 {
			t.Fatalf("commits = %+v, want none", body.Commits)
		}
	})
}

// Regression guard for the resolution moving in front of the reads: a
// revision that does resolve still lists its files, its README and its
// history.
func TestUIListings_KnownRevisionStillLists(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.repo("alice", "foo")
	head := f.commit(repo, "main", "Second commit")

	t.Run("tree", func(t *testing.T) {
		resp := f.do("GET", "/api/v1/repos/model/alice/foo/tree/main", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var body struct {
			Entries []struct {
				Name string `json:"name"`
			} `json:"entries"`
			Readme       *string `json:"readme"`
			LatestCommit *struct {
				OID string `json:"oid"`
			} `json:"latest_commit"`
		}
		resp.json(t, &body)
		if len(body.Entries) != 1 || body.Entries[0].Name != "README.md" {
			t.Fatalf("entries = %+v, want just README.md", body.Entries)
		}
		if body.Readme == nil || *body.Readme != "Second commit" {
			t.Fatalf("readme = %v, want the committed body", body.Readme)
		}
		if body.LatestCommit == nil || body.LatestCommit.OID != head.String() {
			t.Fatalf("latest_commit = %+v, want %s", body.LatestCommit, head)
		}
	})

	t.Run("commits", func(t *testing.T) {
		resp := f.do("GET", "/api/v1/repos/model/alice/foo/commits/main", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var body struct {
			Commits []struct {
				OID string `json:"oid"`
			} `json:"commits"`
		}
		resp.json(t, &body)
		if len(body.Commits) != 2 || body.Commits[0].OID != head.String() {
			t.Fatalf("commits = %+v, want both, newest first", body.Commits)
		}
	})
}

// ------------------------------------------- single-file reads on a bad rev

// fileReadsOf names the three single-file reads that map their errors through
// handleStoreError. gitrepo.Stat reports an unresolvable revision as
// ErrEmptyRepo, which used to fall through to the default branch of that
// helper -- so a typo'd branch answered 500 internal_error.
func fileReadsOf(rev string) []struct{ name, path string } {
	return []struct{ name, path string }{
		{"raw", "/api/v1/raw/model/alice/foo/" + rev + "/README.md"},
		{"model-meta", "/api/v1/model-meta/model/alice/foo/" + rev + "/model.safetensors"},
		{"parquet-schema", "/api/v1/parquet/model/alice/foo/schema/" + rev + "/data.parquet"},
	}
}

func TestUIFileReads_UnknownRevisionIsNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	for _, tc := range fileReadsOf("no-such-branch") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("GET", tc.path, "", nil)
			if resp.status() != 404 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}
}

// A repository with no commits has no file to serve either, so these three
// answer 404 there as well -- what must not happen is the 500 both cases used
// to produce.
func TestUIFileReads_EmptyRepositoryIsNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.emptyRepo("alice", "foo")

	for _, tc := range fileReadsOf("main") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("GET", tc.path, "", nil)
			if resp.status() != 404 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}
}

// Regression guard: a file that is really there on a revision that resolves
// still comes back.
func TestUIRawRead_KnownRevisionStillServesTheFile(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	resp := f.do("GET", "/api/v1/raw/model/alice/foo/main/README.md", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body struct {
		Content string `json:"content"`
	}
	resp.json(t, &body)
	if body.Content != "Add README" {
		t.Fatalf("content = %q, want the committed body", body.Content)
	}
}
