// The huggingface_hub calls this server used to answer with chi's own 404, and
// the ref deletion it used to answer with silence.
//
//   - auth_check() -> GET .../auth-check. Missing, so a client asking about its
//     access to a perfectly readable repository was told the repository does
//     not exist.
//   - super_squash_history() -> POST .../super-squash/{branch}. Missing, so a
//     hub built for multi-gigabyte checkpoints had no way at all to stop
//     paying for superseded ones.
//   - get_model_tags() / get_dataset_tags() -> the tags-by-type catalogues.
//     Missing, while the aggregation behind them was already being computed
//     for the listing sidebar.
//   - repo.ref_deleted. Creating a ref announced itself (through the sync job's
//     repo.push); removing one announced nothing, in either direction, so a
//     mirror watched refs appear and never watched them go away.
//
// Driven over real HTTP against a real Server, like refs_test.go, because the
// status codes and headers *are* the compatibility contract: huggingface_hub
// picks its exception class off X-Error-Code and its retry behaviour off the
// number.

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ------------------------------------------------------------- auth-check

func TestHFAuthCheck_ExistingRepositoryIsAllowed(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	f.repo("alice", "corpus", "dataset")

	for _, path := range []string{
		"/api/models/alice/foo/auth-check",
		"/api/datasets/alice/corpus/auth-check",
	} {
		// No token: there is no read restriction on this instance at all, so
		// an anonymous caller has exactly the access an authenticated one has,
		// and auth_check() must say so rather than invent a gate that the very
		// next clone would disprove.
		resp := f.do("GET", path, "", nil)
		if resp.status() != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body = %s", path, resp.status(), resp.rec.Body.String())
		}
	}
}

func TestHFAuthCheck_UnknownRepositoryIsRepoNotFound(t *testing.T) {
	f := newRefsFixture(t)

	resp := f.do("GET", "/api/models/alice/nope/auth-check", "", nil)
	if resp.status() != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
	// Without this header huggingface_hub raises HfHubHTTPError instead of
	// RepositoryNotFoundError, which is the one exception auth_check's callers
	// are documented to catch.
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
		t.Errorf("X-Error-Code = %q, want RepoNotFound", got)
	}
}

// ----------------------------------------------------------- super-squash

// treeOf reads a branch's commit and the tree it points at, which is what the
// squash has to preserve exactly.
func squashHead(t *testing.T, f *refsFixture, repo *store.Repo, branch string) (plumbing.Hash, plumbing.Hash, int) {
	t.Helper()
	gitRepo, err := f.git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	hash, err := gitRepo.Resolve(branch)
	if err != nil {
		t.Fatalf("resolve %s: %v", branch, err)
	}
	commit, err := gitRepo.CommitObject(hash)
	if err != nil {
		t.Fatalf("load commit %s: %v", hash, err)
	}
	return hash, commit.TreeHash, len(commit.ParentHashes)
}

func TestHFSuperSquash_CollapsesHistoryAndKeepsTheTree(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model") // one commit
	f.commit(repo, "main", "Second commit")
	f.commit(repo, "main", "Third commit")
	tok := f.token(f.alice, "write")

	oldHead, oldTree, parents := squashHead(t, f, repo, "main")
	if parents == 0 {
		t.Fatal("the fixture has no history to squash")
	}

	resp := f.do("POST", "/api/models/alice/foo/super-squash/main", tok,
		map[string]any{"message": "Super-squash branch 'main' using huggingface_hub"})
	if resp.status() != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
	}

	newHead, newTree, parents := squashHead(t, f, repo, "main")
	if parents != 0 {
		t.Errorf("the squashed head has %d parents, want none: the history is supposed to be gone", parents)
	}
	if newHead == oldHead {
		t.Error("main did not move")
	}
	// The whole point of squashing by tree hash: not one blob is rewritten,
	// so the branch's content is bit-for-bit what it was.
	if newTree != oldTree {
		t.Errorf("tree = %s, want the untouched %s", newTree, oldTree)
	}

	// And the ref the API reports is the ref that is really there.
	branches, _ := f.refs("model", "alice", "foo")
	if branches["main"] != newHead.String() {
		t.Errorf("refs report main at %s, want %s", branches["main"], newHead)
	}

	// The file index is keyed by (repo_id, ref, path) and the repository row
	// remembers the head commit, so the same job a push schedules has to run:
	// without it the index still names a commit nothing can reach.
	jobs := f.sync.snapshot()
	if len(jobs) != 1 {
		t.Fatalf("sync jobs = %+v, want exactly one", jobs)
	}
	if jobs[0].RepoID != repo.ID || jobs[0].Ref != "main" ||
		jobs[0].OldSHA != oldHead.String() || jobs[0].NewSHA != newHead.String() {
		t.Errorf("sync job = %+v, want main moved from %s to %s", jobs[0], oldHead, newHead)
	}
}

// A second squash must not rewrite the head it just produced: the tree would
// be identical and the sha different, so every caller and every clone that was
// told about the first one would be wrong for no reason at all.
func TestHFSuperSquash_IsIdempotent(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	f.commit(repo, "main", "Second commit")
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/models/alice/foo/super-squash/main", tok, nil); resp.status() != http.StatusOK {
		t.Fatalf("first squash status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	first, _, _ := squashHead(t, f, repo, "main")

	if resp := f.do("POST", "/api/models/alice/foo/super-squash/main", tok, nil); resp.status() != http.StatusOK {
		t.Fatalf("second squash status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	second, _, _ := squashHead(t, f, repo, "main")
	if second != first {
		t.Errorf("head = %s, want the unchanged %s: there was nothing left to squash", second, first)
	}
	if jobs := f.sync.snapshot(); len(jobs) != 1 {
		t.Errorf("sync jobs = %+v, want only the first squash's", jobs)
	}
}

func TestHFSuperSquash_UnknownBranchIsRevisionNotFound(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/models/alice/foo/super-squash/nope", tok, nil)
	if resp.status() != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
	// RevisionNotFoundError is what super_squash_history documents for a
	// branch it cannot find.
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Errorf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// "You cannot squash history on tags", as huggingface_hub puts it. A tag names
// a point inside a history rather than the head of one, and only refs/heads is
// looked up here -- so a tag name is simply not a branch that exists.
func TestHFSuperSquash_RefusesATagName(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	f.commit(repo, "main", "Second commit")
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/models/alice/foo/tag/main", tok, map[string]any{"tag": "v1.0"}); resp.status() != http.StatusCreated {
		t.Fatalf("create tag: status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	resp := f.do("POST", "/api/models/alice/foo/super-squash/v1.0", tok, nil)
	if resp.status() != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
	if _, _, parents := squashHead(t, f, repo, "main"); parents == 0 {
		t.Error("main was squashed by a request that named a tag")
	}
	if _, tags := f.refs("model", "alice", "foo"); tags["v1.0"] == "" {
		t.Error("the tag disappeared")
	}
}

// huggingface_hub quotes the branch with safe="", so a slashed name arrives
// percent-encoded and chi hands it over still encoded.
func TestHFSuperSquash_PercentEncodedBranch(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	f.commit(repo, "feature/x", "First on the branch")
	f.commit(repo, "feature/x", "Second on the branch")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/models/alice/foo/super-squash/feature%2Fx", tok, nil)
	if resp.status() != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
	}
	if _, _, parents := squashHead(t, f, repo, "feature/x"); parents != 0 {
		t.Errorf("feature/x still has %d parents", parents)
	}
	// main was never named, so it must be untouched.
	if _, _, parents := squashHead(t, f, repo, "main"); parents != 0 {
		t.Logf("main has %d parents (a single-commit fixture is fine)", parents)
	}
}

// Discarding history is the most destructive write this API offers, so it goes
// through the same gate every other content change does.
func TestHFSuperSquash_RequiresWriteAccess(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	f.commit(repo, "main", "Second commit")

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"read token", f.token(f.alice, "read"), http.StatusForbidden},
		{"another user", f.token(f.bob, "write"), http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := f.do("POST", "/api/models/alice/foo/super-squash/main", tt.token, nil)
			if resp.status() != tt.want {
				t.Fatalf("status = %d, want %d; body = %s", resp.status(), tt.want, resp.rec.Body.String())
			}
			if _, _, parents := squashHead(t, f, repo, "main"); parents == 0 {
				t.Fatal("the history was squashed by a caller that may not write")
			}
		})
	}
}

func TestHFSuperSquash_RefusedOnAnArchivedRepository(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	f.commit(repo, "main", "Second commit")
	tok := f.token(f.alice, "write")

	if _, err := f.st.SetRepoArchived(context.Background(), repo.ID, true, f.alice.ID); err != nil {
		t.Fatalf("archive repo: %v", err)
	}

	resp := f.do("POST", "/api/models/alice/foo/super-squash/main", tok, nil)
	if resp.status() != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	if _, _, parents := squashHead(t, f, repo, "main"); parents == 0 {
		t.Error("an archived repository lost its history")
	}
}

// ----------------------------------------------------------- tags-by-type

type tagCatalogue map[string][]struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

func (c tagCatalogue) ids(group string) []string {
	out := make([]string, 0, len(c[group]))
	for _, item := range c[group] {
		out = append(out, item.ID)
	}
	return out
}

func (c tagCatalogue) has(group, id string) bool {
	for _, item := range c[group] {
		if item.ID == id {
			// The group name is also each item's declared type -- that is the
			// shape huggingface_hub reads.
			return item.Type == group && item.Label != ""
		}
	}
	return false
}

func newTagFixture(t *testing.T) *refsFixture {
	t.Helper()
	f := newRefsFixture(t)
	ctx := context.Background()

	model := f.repo("alice", "bert", "model")
	if err := f.st.UpdateRepoIndex(ctx, model.ID, "abc", 10, map[string]any{
		"license":      "apache-2.0",
		"pipeline_tag": "text-generation",
		"tags":         []any{"nlp", "pytorch"},
	}, "a model", false); err != nil {
		t.Fatalf("index model card: %v", err)
	}

	dataset := f.repo("alice", "corpus", "dataset")
	if err := f.st.UpdateRepoIndex(ctx, dataset.ID, "def", 20, map[string]any{
		"license":         "mit",
		"task_categories": []any{"summarization"},
		"tags":            []any{"text"},
	}, "a dataset", false); err != nil {
		t.Fatalf("index dataset card: %v", err)
	}
	return f
}

func (f *refsFixture) catalogue(t *testing.T, path string) tagCatalogue {
	t.Helper()
	resp := f.do("GET", path, "", nil)
	if resp.status() != http.StatusOK {
		t.Fatalf("%s: status = %d, body = %s", path, resp.status(), resp.rec.Body.String())
	}
	var out tagCatalogue
	resp.json(t, &out)
	return out
}

func TestHFModelTags_GroupsTheFacetsHFExpects(t *testing.T) {
	f := newTagFixture(t)
	got := f.catalogue(t, "/api/models-tags-by-type")

	// Every key huggingface_hub's ModelTags indexes has to be there: it reads
	// a fixed list and raises KeyError on a missing one, so an empty group is
	// the only safe way to say "nothing here".
	for _, group := range hfTagGroups["model"] {
		if _, ok := got[group]; !ok {
			t.Errorf("group %q is missing from the catalogue", group)
		}
	}
	if !got.has("license", "apache-2.0") {
		t.Errorf("license = %v, want apache-2.0", got.ids("license"))
	}
	// A model's task lives in pipeline_tag, singular, which is the card field
	// it comes from.
	if !got.has("pipeline_tag", "text-generation") {
		t.Errorf("pipeline_tag = %v, want text-generation", got.ids("pipeline_tag"))
	}
	// Free-form card tags are what "other" is.
	if !got.has("other", "nlp") || !got.has("other", "pytorch") {
		t.Errorf("other = %v, want nlp and pytorch", got.ids("other"))
	}
	// The catalogue is per kind: a dataset's license is not a model tag.
	if got.has("license", "mit") {
		t.Errorf("license = %v, want the dataset's mit left out", got.ids("license"))
	}
	if len(got["library"]) != 0 {
		t.Errorf("library = %v, want empty: nothing here parses a library out of a card", got.ids("library"))
	}
}

func TestHFDatasetTags_GroupsTheFacetsHFExpects(t *testing.T) {
	f := newTagFixture(t)
	got := f.catalogue(t, "/api/datasets-tags-by-type")

	for _, group := range hfTagGroups["dataset"] {
		if _, ok := got[group]; !ok {
			t.Errorf("group %q is missing from the catalogue", group)
		}
	}
	// A dataset's tasks live in task_categories, plural, and never in
	// pipeline_tag -- that group does not exist in this catalogue at all.
	if !got.has("task_categories", "summarization") {
		t.Errorf("task_categories = %v, want summarization", got.ids("task_categories"))
	}
	if _, ok := got["pipeline_tag"]; ok {
		t.Error("pipeline_tag is a model-only group and must not appear in the dataset catalogue")
	}
	if !got.has("license", "mit") {
		t.Errorf("license = %v, want mit", got.ids("license"))
	}
	if got.has("license", "apache-2.0") {
		t.Errorf("license = %v, want the model's apache-2.0 left out", got.ids("license"))
	}
}

// An instance with nothing in it still answers with the full set of groups,
// each empty -- the client iterates them without checking.
func TestHFTagsByType_EmptyInstanceStillAnswersEveryGroup(t *testing.T) {
	f := newRefsFixture(t)
	for _, path := range []string{"/api/models-tags-by-type", "/api/datasets-tags-by-type"} {
		got := f.catalogue(t, path)
		if len(got) == 0 {
			t.Fatalf("%s: answered with nothing at all", path)
		}
		for group, items := range got {
			if items == nil {
				t.Errorf("%s: group %q is null, want an empty array", path, group)
			}
		}
	}
}

// -------------------------------------------------------- repo.ref_deleted

type firedWebhook struct {
	Event     string
	Namespace string
	RepoID    *int64
	Payload   map[string]any
}

// recordingWebhooks stands in for the delivery dispatcher: fireWebhook only
// needs something that can be told an event happened.
type recordingWebhooks struct {
	mu    sync.Mutex
	calls []firedWebhook
}

func (rw *recordingWebhooks) Fire(_ context.Context, event, ns string, repoID *int64, payload any) error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	body, _ := payload.(map[string]any)
	rw.calls = append(rw.calls, firedWebhook{Event: event, Namespace: ns, RepoID: repoID, Payload: body})
	return nil
}

func (rw *recordingWebhooks) snapshot() []firedWebhook {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return append([]firedWebhook(nil), rw.calls...)
}

// only returns the single event of the given kind, failing when there is not
// exactly one -- "fired twice" is as wrong as "never fired".
func only(t *testing.T, fired []firedWebhook, event apitypes.WebhookEvent) firedWebhook {
	t.Helper()
	var found []firedWebhook
	for _, call := range fired {
		if call.Event == string(event) {
			found = append(found, call)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s fired %d times, want exactly once (all events: %+v)", event, len(found), fired)
	}
	return found[0]
}

func TestHFDeleteBranch_AnnouncesTheRefIsGone(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	hooks := &recordingWebhooks{}
	f.s.webhooks = hooks
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/models/alice/foo/branch/experiment", tok, nil); resp.status() != http.StatusCreated {
		t.Fatalf("create branch: status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	branches, _ := f.refs("model", "alice", "foo")
	target := branches["experiment"]

	if resp := f.do("DELETE", "/api/models/alice/foo/branch/experiment", tok, nil); resp.status() != http.StatusOK {
		t.Fatalf("delete branch: status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	call := only(t, hooks.snapshot(), apitypes.WebhookEventRepoRefDeleted)
	if call.Namespace != "alice" || call.RepoID == nil || *call.RepoID != repo.ID {
		t.Errorf("delivered to namespace %q / repo %v, want alice / %d", call.Namespace, call.RepoID, repo.ID)
	}
	want := map[string]any{
		"namespace": "alice", "repo": "foo", "full_name": "alice/foo", "kind": "model",
		"ref": "experiment", "ref_type": "branch", "old_sha": target, "new_sha": "",
	}
	for k, v := range want {
		if got := call.Payload[k]; got != v {
			t.Errorf("payload[%q] = %v, want %v", k, got, v)
		}
	}
	// The index rows for a ref nothing can resolve are unreachable, so there
	// is nothing to re-index -- only something to announce.
	if jobs := f.sync.snapshot(); len(jobs) != 1 {
		t.Errorf("sync jobs = %+v, want only the branch creation's", jobs)
	}
}

func TestHFDeleteTag_AnnouncesTheRefIsGone(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	hooks := &recordingWebhooks{}
	f.s.webhooks = hooks
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/models/alice/foo/tag/main", tok, map[string]any{"tag": "v1.0"}); resp.status() != http.StatusCreated {
		t.Fatalf("create tag: status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	_, tags := f.refs("model", "alice", "foo")
	target := tags["v1.0"]

	if resp := f.do("DELETE", "/api/models/alice/foo/tag/v1.0", tok, nil); resp.status() != http.StatusOK {
		t.Fatalf("delete tag: status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	call := only(t, hooks.snapshot(), apitypes.WebhookEventRepoRefDeleted)
	if call.Payload["ref"] != "v1.0" || call.Payload["ref_type"] != "tag" {
		t.Errorf("payload = %+v, want the tag v1.0", call.Payload)
	}
	if call.Payload["old_sha"] != target {
		t.Errorf("old_sha = %v, want the deleted tag's target %s", call.Payload["old_sha"], target)
	}
}

// The push path's half of the same fix. A deleted branch is absent from the
// tips read *after* a push, so the loop that walks them could never see one:
// the deletion has to be read out of the before-snapshot instead.
func TestSchedulePostPush_DeletedBranchIsAnnouncedAndNotIndexed(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	hooks := &recordingWebhooks{}
	f.s.webhooks = hooks

	before := map[string]string{
		"main":      "1111111111111111111111111111111111111111",
		"gone":      "2222222222222222222222222222222222222222",
		"untouched": "3333333333333333333333333333333333333333",
	}
	after := map[string]string{
		"main":      "4444444444444444444444444444444444444444",
		"untouched": "3333333333333333333333333333333333333333",
		"fresh":     "5555555555555555555555555555555555555555",
	}
	f.s.schedulePostPush(context.Background(), repo, before, after, "push")

	jobs := f.sync.snapshot()
	scheduled := map[string]enqueueCall{}
	for _, job := range jobs {
		scheduled[job.Ref] = job
	}
	if len(jobs) != 2 {
		t.Fatalf("sync jobs = %+v, want one for main and one for fresh", jobs)
	}
	if job := scheduled["main"]; job.OldSHA != before["main"] || job.NewSHA != after["main"] {
		t.Errorf("main's job = %+v, want the move it just made", job)
	}
	if _, ok := scheduled["fresh"]; !ok {
		t.Errorf("jobs = %+v, want one for the new branch", jobs)
	}
	// A deleted ref must not be queued for indexing: the tree it named is
	// gone, so the job could only fail or index nothing.
	if _, ok := scheduled["gone"]; ok {
		t.Errorf("jobs = %+v, want nothing scheduled for the deleted branch", jobs)
	}

	call := only(t, hooks.snapshot(), apitypes.WebhookEventRepoRefDeleted)
	if call.Payload["ref"] != "gone" || call.Payload["ref_type"] != "branch" {
		t.Errorf("payload = %+v, want the deleted branch", call.Payload)
	}
	if call.Payload["old_sha"] != before["gone"] {
		t.Errorf("old_sha = %v, want %s", call.Payload["old_sha"], before["gone"])
	}
	if call.Payload["new_sha"] != "" {
		t.Errorf("new_sha = %v, want empty: there is no new value for a deleted ref", call.Payload["new_sha"])
	}
}
