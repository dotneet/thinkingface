package gitrepo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// resolveFixture is a repository with some history on main and one unrelated
// commit, other, on a branch of its own. prefixSource is a commit on main
// whose leading hex digit other does not share, so a ref named after a
// prefix of prefixSource that points at other can only resolve to other if
// refs are consulted first.
type resolveFixture struct {
	repo         *Repo
	prefixSource plumbing.Hash
	other        plumbing.Hash
}

func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	_, repo := newTestRepo(t)
	var history []plumbing.Hash
	for i, content := range []string{"v1\n", "v2\n", "v3\n", "v4\n"} {
		history = append(history, mustCommit(t, repo, "main", "commit "+string(rune('a'+i)), addOp("a.txt", content)))
	}
	other := mustCommit(t, repo, "side", "unrelated", addOp("b.txt", "side\n"))
	for _, h := range history {
		if h.String()[0] != other.String()[0] {
			return resolveFixture{repo: repo, prefixSource: h, other: other}
		}
	}
	t.Skip("every commit on main shares its first hex digit with the side commit")
	return resolveFixture{}
}

// A branch whose name happens to be hex -- "c", "cafe", "2024", "bf16" --
// used to resolve to whichever commit's id started with those characters,
// because go-git's ResolveRevision tries a hash prefix before any ref. The
// syncer then indexed the wrong tree under the branch's name.
func TestResolve_HexLookingBranchNameBeatsHashPrefix(t *testing.T) {
	f := newResolveFixture(t)
	src := f.prefixSource.String()
	for _, name := range []string{src[:1], src[:4], src[:7], src[:12]} {
		if err := f.repo.CreateRef(BranchRef(name), f.other); err != nil {
			t.Fatalf("CreateRef(%s): %v", name, err)
		}
		got, err := f.repo.Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if got != f.other {
			t.Errorf("Resolve(%q) = %s, want the branch tip %s (not the commit the name prefixes)", name, got, f.other)
		}
	}
}

// A branch and a tag of the same name: the branch wins, since every write
// targets a branch and reading anything else back would contradict it. The
// full ref name still reaches the tag.
func TestResolve_BranchBeatsTagOfTheSameName(t *testing.T) {
	f := newResolveFixture(t)
	mainHead, _ := f.repo.Resolve("main")
	if err := f.repo.CreateRef(BranchRef("v1"), mainHead); err != nil {
		t.Fatalf("CreateRef branch: %v", err)
	}
	tagObj, err := f.repo.WriteTagObject("v1", f.other, "release", Signature{Name: "t", Email: "t@example.com"})
	if err != nil {
		t.Fatalf("WriteTagObject: %v", err)
	}
	if err := f.repo.CreateRef(TagRef("v1"), tagObj); err != nil {
		t.Fatalf("CreateRef tag: %v", err)
	}

	for _, tc := range []struct {
		rev  string
		want plumbing.Hash
	}{
		{"v1", mainHead},
		{"refs/heads/v1", mainHead},
		{"refs/tags/v1", f.other}, // peeled through the annotated tag
	} {
		got, err := f.repo.Resolve(tc.rev)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.rev, err)
		}
		if got != tc.want {
			t.Errorf("Resolve(%q) = %s, want %s", tc.rev, got, tc.want)
		}
	}
}

// A full commit id is the one revision that cannot be ambiguous, so it beats
// a branch or tag that is named after it -- as it does in git. Otherwise
// anyone who can create a branch could redirect every read pinned to a commit
// (snapshot_download(revision=<sha>), a trust_remote_code pin) to a tree of
// their choosing.
func TestResolve_FullCommitIDBeatsRefOfTheSameName(t *testing.T) {
	f := newResolveFixture(t)
	full := f.prefixSource.String()
	if err := f.repo.CreateRef(BranchRef(full), f.other); err != nil {
		t.Fatalf("CreateRef branch: %v", err)
	}
	if err := f.repo.CreateRef(TagRef(strings.ToUpper(full)), f.other); err != nil {
		t.Fatalf("CreateRef tag: %v", err)
	}
	for _, rev := range []string{full, strings.ToUpper(full)} {
		got, err := f.repo.Resolve(rev)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", rev, err)
		}
		if got != f.prefixSource {
			t.Errorf("Resolve(%q) = %s, want the commit %s, not the ref's %s", rev, got, f.prefixSource, f.other)
		}
	}
	// The ref is still reachable by its full name.
	if got, _ := f.repo.Resolve(BranchRef(full)); got != f.other {
		t.Errorf("Resolve(refs/heads/<id>) = %s, want %s", got, f.other)
	}

	// A 40-hex name that is not a commit here -- no such object, or a tree --
	// has no other meaning, so the ref of that name answers.
	commit, err := f.repo.CommitObject(f.prefixSource)
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	for _, name := range []string{strings.Repeat("e", 40), commit.TreeHash.String()} {
		if err := f.repo.CreateRef(BranchRef(name), f.other); err != nil {
			t.Fatalf("CreateRef(%s): %v", name, err)
		}
		if got, err := f.repo.Resolve(name); err != nil || got != f.other {
			t.Errorf("Resolve(%q) = %s, %v; want the branch tip %s", name, got, err, f.other)
		}
	}
}

// git accepts a ref component starting with "-" (refs/heads/-dash is what
// `git push origin HEAD:refs/heads/-dash` creates); go-git's Validate does
// not. Resolve must only refuse names that could escape refs/, not every name
// go-git would not have created itself.
func TestResolve_RefNamesGitAcceptsButGoGitRejects(t *testing.T) {
	f := newResolveFixture(t)
	runGit(t, f.repo.Dir(), "update-ref", "refs/heads/-dash", f.other.String())
	runGit(t, f.repo.Dir(), "update-ref", "refs/tags/-t", f.other.String())
	for _, rev := range []string{"-dash", "refs/heads/-dash", "-t", "refs/tags/-t"} {
		got, err := f.repo.Resolve(rev)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", rev, err)
		}
		if got != f.other {
			t.Errorf("Resolve(%q) = %s, want %s", rev, got, f.other)
		}
	}
}

// Commit ids: a full id resolves in either case, an abbreviation needs at
// least seven digits, and a short one is not even looked up -- a one-digit
// "prefix" used to walk every pack index for an unauthenticated GET.
func TestResolve_CommitIDs(t *testing.T) {
	f := newResolveFixture(t)
	full := f.prefixSource.String()

	for _, rev := range []string{full, strings.ToUpper(full), full[:7], full[:9]} {
		got, err := f.repo.Resolve(rev)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", rev, err)
		}
		if got != f.prefixSource {
			t.Errorf("Resolve(%q) = %s, want %s", rev, got, f.prefixSource)
		}
	}
	for _, rev := range []string{full[:1], full[:4], full[:6]} {
		if _, err := f.repo.Resolve(rev); !errors.Is(err, ErrEmptyRepo) {
			t.Errorf("Resolve(%q) err = %v, want ErrEmptyRepo (too short to be an abbreviation)", rev, err)
		}
	}
}

// Only commits resolve: a tree or blob id, an id that is not in the
// repository, revision expressions, and names that would escape refs/.
func TestResolve_RejectsNonCommits(t *testing.T) {
	f := newResolveFixture(t)
	commit, err := f.repo.CommitObject(f.prefixSource)
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	for _, rev := range []string{
		commit.TreeHash.String(),
		strings.Repeat("e", 40),
		"main~1",
		"main^",
		"../../HEAD",
		"refs/heads/../../config",
		"refs/heads/./main",
		"refs//heads/main",
		"/refs/heads/main",
		"main\x00",
		"no-such-branch",
	} {
		if _, err := f.repo.Resolve(rev); !errors.Is(err, ErrEmptyRepo) {
			t.Errorf("Resolve(%q) err = %v, want ErrEmptyRepo", rev, err)
		}
	}
}

// A repository whose history lives only on a branch HEAD does not point at
// (a first upload with revision="dev", `git push origin master` when the
// default is main) has an unborn HEAD but is not empty. IsEmpty used to ask
// HEAD, so the API answered an unknown revision there as an empty listing.
func TestIsEmpty_HistoryOnlyOnANonHEADBranch(t *testing.T) {
	_, repo := newTestRepo(t)
	if !repo.IsEmpty() {
		t.Fatal("a fresh repository must be empty")
	}
	mustCommit(t, repo, "dev", "first", addOp("a.txt", "v1\n"))

	if _, err := repo.Resolve("HEAD"); !errors.Is(err, ErrEmptyRepo) {
		t.Fatalf("Resolve(HEAD) err = %v, want ErrEmptyRepo (HEAD is still unborn)", err)
	}
	if repo.IsEmpty() {
		t.Fatal("IsEmpty = true for a repository with a branch")
	}
}

// A tag alone is history too.
func TestIsEmpty_TagOnly(t *testing.T) {
	_, repo := newTestRepo(t)
	h := mustCommit(t, repo, "tmp", "first", addOp("a.txt", "v1\n"))
	if err := repo.CreateRef(TagRef("v1"), h); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}
	if _, err := repo.DeleteRef(BranchRef("tmp")); err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}
	if repo.IsEmpty() {
		t.Fatal("IsEmpty = true for a repository with a tag")
	}
}

// writeWALState stamps repo's directory with a WAL state file recording refs,
// the way a materialisation (or AdoptLocal after a successful write) leaves
// it. ResetBranch reads nothing else from the WAL.
func writeWALState(t *testing.T, repo *Repo, refs map[string]string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"generation": 42, "base": "", "applied": []string{}, "refs": refs, "seq": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir(), wal.StateFileName), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two local commits on one branch can interleave with their WAL writes: A
// advances main X->A, B advances A->B, A's write fails for a non-stale reason
// (a GCS 5xx), and B's write is then stale because the index still says X.
//
// A's rollback must leave main alone -- B has moved it on. B's rollback must
// land on X, the value the WAL holds, not on A, B's parent: A is a commit no
// index contains, and with the index generation unchanged no materialisation
// would ever move main off it again, so every later commit on the branch
// would be rejected as stale.
func TestResetBranch_ChainedCommitsRollBackToTheWALValue(t *testing.T) {
	_, repo := newTestRepo(t)
	x := mustCommit(t, repo, "main", "x", addOp("a.txt", "x\n"))
	writeWALState(t, repo, map[string]string{"refs/heads/main": x.String()})
	a := mustCommit(t, repo, "main", "a", addOp("a.txt", "a\n"))
	b := mustCommit(t, repo, "main", "b", addOp("a.txt", "b\n"))

	if err := repo.ResetBranch("main", a, x); err != nil {
		t.Fatalf("ResetBranch(expect=a): %v", err)
	}
	if got, _ := repo.Resolve("main"); got != b {
		t.Fatalf("main = %s, want B's commit %s left alone", got, b)
	}

	if err := repo.ResetBranch("main", b, a); err != nil {
		t.Fatalf("ResetBranch(expect=b): %v", err)
	}
	if got, _ := repo.Resolve("main"); got != x {
		t.Fatalf("main = %s, want the WAL's %s (A, %s, was never accepted)", got, x, a)
	}
}

// A WAL-managed copy with no state file is one EnsureLocal found no index for,
// so the WAL's value for every ref is "absent". Rolling a chained pair of first
// commits back to the parent would leave a ref no index has, and the next
// EnsureLocal would refuse the whole copy as ErrIndexMissing.
func TestResetBranch_WALManagedWithoutIndexDeletes(t *testing.T) {
	_, repo := newTestRepo(t)
	repo.walManaged = true
	a := mustCommit(t, repo, "main", "a", addOp("a.txt", "a\n"))
	b := mustCommit(t, repo, "main", "b", addOp("a.txt", "b\n"))

	if err := repo.ResetBranch("main", b, a); err != nil {
		t.Fatalf("ResetBranch: %v", err)
	}
	if ok, _ := repo.HasBranch("main"); ok {
		t.Fatal("main survived although the WAL has never held it")
	}
}

// A repository the WAL does not manage has no recorded value to prefer, and
// rolls back to the caller's parent.
func TestResetBranch_UnmanagedFallsBackToParent(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "x", addOp("a.txt", "x\n"))
	a := mustCommit(t, repo, "main", "a", addOp("a.txt", "a\n"))
	b := mustCommit(t, repo, "main", "b", addOp("a.txt", "b\n"))

	if err := repo.ResetBranch("main", b, a); err != nil {
		t.Fatalf("ResetBranch: %v", err)
	}
	if got, _ := repo.Resolve("main"); got != a {
		t.Fatalf("main = %s, want the parent %s", got, a)
	}
}

// The first-commit rollback deletes the ref, under the same compare.
func TestResetBranch_ZeroTargetDeletesOnlyWhenUnmoved(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "seed", addOp("a.txt", "x\n"))
	c := mustCommit(t, repo, "topic", "c", addOp("b.txt", "c\n"))
	d := mustCommit(t, repo, "topic", "d", addOp("b.txt", "d\n"))

	if err := repo.ResetBranch("topic", c, plumbing.ZeroHash); err != nil {
		t.Fatalf("ResetBranch(expect=c): %v", err)
	}
	if ok, _ := repo.HasBranch("topic"); !ok {
		t.Fatal("topic was deleted although it had moved on to another commit")
	}
	if err := repo.ResetBranch("topic", d, plumbing.ZeroHash); err != nil {
		t.Fatalf("ResetBranch(expect=d): %v", err)
	}
	if ok, _ := repo.HasBranch("topic"); ok {
		t.Fatal("topic survived its own rollback")
	}
	// Nothing left to roll back is not an error.
	if err := repo.ResetBranch("topic", d, plumbing.ZeroHash); err != nil {
		t.Fatalf("ResetBranch on a missing branch: %v", err)
	}
}
