// The two download counters the repository page shows side by side -- the
// all-time total (repositories.downloads) and the last-30-days figure summed
// out of repo_download_stats -- used to be incremented at different points in
// handleResolve: the 30-day one once per request, the total only on the paths
// that transferred a body. huggingface_hub HEADs every file before fetching
// it, so a real client drove the window above the total it is a window of
// (measured on admin/tf-e2e-imdb: downloads = 2, downloads_last_30_days = 3).
// These tests pin the single rule that replaces it: one resolve request is one
// count, on both counters, for HEAD and for the LFS path too.

package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// readCounters reads both counters the way their two consumers do: the repo
// row is what the HF-compatible payload's "downloads" comes from, and the
// 30-day window is the same DownloadsSince(-30d) call handleGetRepo makes.
func readCounters(t *testing.T, f *secFixture, repoID int64) (total, window int64) {
	t.Helper()
	ctx := context.Background()
	r, err := f.st.GetRepoByID(ctx, repoID)
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}
	window, err = f.st.DownloadsSince(ctx, repoID, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("downloads since: %v", err)
	}
	return r.Downloads, window
}

// awaitCounters waits for both counters to reach want. Both writes happen in a
// goroutine detached from the request (so a client hanging up can neither lose
// nor delay them), which means the assertion cannot read straight after the
// response without racing it -- and a fixed sleep would be either flaky or
// slow. Polling to a deadline instead settles as soon as the writes land, and
// fails with the actual pair of numbers when they never do.
func awaitCounters(t *testing.T, f *secFixture, repoID, want int64) (total, window int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		total, window = readCounters(t, f, repoID)
		if total >= want && window >= want {
			// Returning on >= rather than == is deliberate: an overcount has
			// to reach the caller's equality check, not spin here until the
			// deadline and report a timeout for it.
			return total, window
		}
		if time.Now().After(deadline) {
			t.Fatalf("counters stuck at total=%d, window=%d after 10s; want %d for both", total, window, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// assertCounters waits for the counters and checks them against want, plus the
// invariant that made this a bug: the window can never exceed the total it is
// a window of.
func assertCounters(t *testing.T, f *secFixture, repoID, want int64, after string) {
	t.Helper()
	total, window := awaitCounters(t, f, repoID, want)
	if total != want || window != want {
		t.Errorf("after %s: downloads = %d, last 30 days = %d; want %d for both", after, total, window, want)
	}
	if total < window {
		t.Errorf("after %s: all-time downloads (%d) is below the 30-day window (%d), which is impossible by definition",
			after, total, window)
	}
}

func TestResolve_HeadAndGetAdvanceBothDownloadCounters(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte("a,b\n1,2\n"))

	if total, window := readCounters(t, f, repo.ID); total != 0 || window != 0 {
		t.Fatalf("fresh repo starts at downloads = %d, last 30 days = %d; want 0 for both", total, window)
	}

	// A HEAD is what hf_hub_download issues before every download
	// (get_hf_file_metadata). It transfers no body, but it is still a request
	// this repository served, so it counts once -- on both counters.
	if rec := f.do(secRequest{method: "HEAD", path: "/datasets/alice/data/resolve/main/rows.csv"}); rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertCounters(t, f, repo.ID, 1, "one HEAD")

	if rec := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/main/rows.csv"}); rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertCounters(t, f, repo.ID, 2, "a HEAD then a GET")

	// A ranged GET is still one request, not one per slice.
	rec := f.do(secRequest{
		method:  "GET",
		path:    "/datasets/alice/data/resolve/main/rows.csv",
		headers: map[string]string{"Range": "bytes=0-2"},
	})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("ranged GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertCounters(t, f, repo.ID, 3, "a HEAD, a GET and a ranged GET")
}

// The LFS path leaves the server before any body moves -- a 302 to a signed
// URL in production, a proxied stream under the emulator -- so it is exactly
// the path the total used to miss on a HEAD.
func TestResolveLFS_HeadAndGetAdvanceBothDownloadCounters(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("hello lfs")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)), func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(body)) + "\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)

	if rec := f.do(secRequest{method: "HEAD", path: "/alice/weights/resolve/main/model.bin"}); rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertCounters(t, f, repo.ID, 1, "one LFS HEAD")

	if rec := f.do(secRequest{method: "GET", path: "/alice/weights/resolve/main/model.bin"}); rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertCounters(t, f, repo.ID, 2, "an LFS HEAD then an LFS GET")
}

// A request that never reaches a servable file counts on neither counter: the
// increment sits behind the directory check, so a 404 and a directory leave
// both at zero rather than one of them at one.
func TestResolve_FailedRequestsCountOnNeitherCounter(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "nested/rows.csv", []byte("a,b\n1,2\n"))

	for _, tc := range []struct{ name, path string }{
		{"missing file", "/datasets/alice/data/resolve/main/nope.csv"},
		{"directory", "/datasets/alice/data/resolve/main/nested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := f.do(secRequest{method: "GET", path: tc.path}); rec.Code == http.StatusOK {
				t.Fatalf("status = %d, want a failure", rec.Code)
			}
		})
	}

	// One real request afterwards: once its count has landed, any spurious
	// count from the two failures above would already be visible, so this
	// needs no waiting of its own beyond the poll.
	if rec := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/main/nested/rows.csv"}); rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertCounters(t, f, repo.ID, 1, "two failed requests and one served GET")
}

// Guard against the counters drifting apart again: whatever the mix of
// requests, the all-time total must come out at least as large as the 30-day
// sum, because every count goes to both.
func TestResolve_TotalNeverFallsBelowThirtyDayWindow(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte("a,b\n1,2\n"))

	requests := []secRequest{
		{method: "HEAD", path: "/datasets/alice/data/resolve/main/rows.csv"},
		{method: "GET", path: "/datasets/alice/data/resolve/main/rows.csv"},
		{method: "HEAD", path: "/datasets/alice/data/resolve/main/rows.csv"},
		{method: "GET", path: "/datasets/alice/data/resolve/main/rows.csv",
			headers: map[string]string{"Range": "bytes=-3"}},
		{method: "HEAD", path: "/datasets/alice/data/resolve/main/rows.csv"},
	}
	for _, req := range requests {
		if rec := f.do(req); rec.Code != http.StatusOK && rec.Code != http.StatusPartialContent {
			t.Fatalf("%s status = %d, body = %s", req.method, rec.Code, rec.Body.String())
		}
	}
	assertCounters(t, f, repo.ID, int64(len(requests)), "a HEAD-heavy download session")
}
