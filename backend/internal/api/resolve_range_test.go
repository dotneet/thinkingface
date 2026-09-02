// Range handling on the resolve endpoint for plain git blobs. Files that no
// .gitattributes rule sends to LFS (.csv / .json / .jsonl / .txt / README.md
// under the inline threshold) come back through the git path, which used to
// answer 200 with the whole body no matter what Range said -- a range-reading
// client that trusts the body over the status code would silently read the
// wrong bytes.

package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// rangeBlob is 26 bytes, so every offset in the tests below is unambiguous.
const rangeBlob = "abcdefghijklmnopqrstuvwxyz"

func newRangeFixture(t *testing.T) *secFixture {
	t.Helper()
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte(rangeBlob))
	f.writeFile(repo, "empty.txt", nil)
	return f
}

func TestResolve_RangeServesPartialContent(t *testing.T) {
	f := newRangeFixture(t)

	tests := []struct {
		name      string
		rangeHdr  string
		wantRange string
		wantBody  string
	}{
		{"closed", "bytes=4-7", "bytes 4-7/26", "efgh"},
		{"open ended", "bytes=8-", "bytes 8-25/26", rangeBlob[8:]},
		{"suffix", "bytes=-4", "bytes 22-25/26", "wxyz"},
		{"single byte", "bytes=0-0", "bytes 0-0/26", "a"},
		// A closed range running past the end clamps to the last byte rather
		// than failing -- what a client resuming a download sends.
		{"end past eof", "bytes=20-99", "bytes 20-25/26", rangeBlob[20:]},
		// A suffix longer than the file is the whole file, still as a 206.
		{"suffix past eof", "bytes=-99", "bytes 0-25/26", rangeBlob},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(secRequest{
				method:  "GET",
				path:    "/datasets/alice/data/resolve/main/rows.csv",
				headers: map[string]string{"Range": tt.rangeHdr},
			})
			if rec.Code != http.StatusPartialContent {
				t.Fatalf("status = %d, want 206; body = %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Range"); got != tt.wantRange {
				t.Errorf("Content-Range = %q, want %q", got, tt.wantRange)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			want := strconv.Itoa(len(tt.wantBody))
			if got := rec.Header().Get("Content-Length"); got != want {
				t.Errorf("Content-Length = %q, want %q", got, want)
			}
			// The identity headers a partial response still has to carry.
			if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Errorf("Accept-Ranges = %q, want bytes", got)
			}
			if rec.Header().Get("ETag") == "" {
				t.Error("ETag is missing from the partial response")
			}
			if rec.Header().Get("X-Repo-Commit") == "" {
				t.Error("X-Repo-Commit is missing from the partial response")
			}
		})
	}
}

// A range this server cannot make sense of degrades to the whole file, the
// same fallback the LFS path has (RFC 9110 §14.2 allows ignoring a Range that
// cannot be understood). A range that *is* understood but starts past the end
// is different -- see TestResolve_RangePastEndIsNotSatisfiable.
func TestResolve_UnusableRangeReturnsWholeBody(t *testing.T) {
	f := newRangeFixture(t)

	tests := []struct {
		name     string
		path     string
		rangeHdr string
		wantBody string
	}{
		{"malformed", "rows.csv", "bytes=abc", rangeBlob},
		{"no unit", "rows.csv", "0-4", rangeBlob},
		{"end before start", "rows.csv", "bytes=9-4", rangeBlob},
		{"multi range", "rows.csv", "bytes=0-1,4-5", rangeBlob},
		{"negative suffix of empty file", "empty.txt", "bytes=-4", ""},
		{"no range header", "rows.csv", "", rangeBlob},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := map[string]string{}
			if tt.rangeHdr != "" {
				headers["Range"] = tt.rangeHdr
			}
			rec := f.do(secRequest{
				method:  "GET",
				path:    "/datasets/alice/data/resolve/main/" + tt.path,
				headers: headers,
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Range"); got != "" {
				t.Errorf("Content-Range = %q, want it unset on a 200", got)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			want := strconv.Itoa(len(tt.wantBody))
			if got := rec.Header().Get("Content-Length"); got != want {
				t.Errorf("Content-Length = %q, want %q", got, want)
			}
		})
	}
}

// A well-formed range whose first byte is at or past the end of the file is
// 416, never a 200 carrying the whole body.
//
// This is the one Range case that cannot be answered leniently.
// huggingface_hub's http_get resumes a download by sending `Range: bytes=N-`
// and appending the response to the ".incomplete" file it already holds --
// without checking that the status is 206. If the previous attempt had already
// transferred everything, N equals the file's size, and a 200 makes it append
// a second whole copy to a complete file. The result fails its hash check, and
// each retry doubles the file again.
func TestResolve_RangePastEndIsNotSatisfiable(t *testing.T) {
	f := newRangeFixture(t)

	tests := []struct {
		name     string
		rangeHdr string
	}{
		// Exactly the resume case: the client already holds all 26 bytes.
		{"open ended at eof", "bytes=26-"},
		{"open ended past eof", "bytes=99-"},
		{"closed past eof", "bytes=30-40"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(secRequest{
				method:  "GET",
				path:    "/datasets/alice/data/resolve/main/rows.csv",
				headers: map[string]string{"Range": tt.rangeHdr},
			})
			if rec.Code != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("status = %d, want 416; body = %q", rec.Code, rec.Body.String())
			}
			// The real length, so a client that was resuming can see it
			// already holds the whole file (RFC 9110 §14.4).
			want := "bytes */" + strconv.Itoa(len(rangeBlob))
			if got := rec.Header().Get("Content-Range"); got != want {
				t.Errorf("Content-Range = %q, want %q", got, want)
			}
			if got := rec.Body.String(); strings.Contains(got, rangeBlob) {
				t.Errorf("body = %q, want no file content in a 416", got)
			}
		})
	}
}

// hf_hub_download HEADs a file to learn its real size before fetching it, so a
// Range must not shrink what HEAD reports.
func TestResolve_HeadIgnoresRange(t *testing.T) {
	f := newRangeFixture(t)

	rec := f.do(secRequest{
		method:  "HEAD",
		path:    "/datasets/alice/data/resolve/main/rows.csv",
		headers: map[string]string{"Range": "bytes=4-7"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(rangeBlob)) {
		t.Errorf("Content-Length = %q, want %d", got, len(rangeBlob))
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Errorf("Content-Range = %q, want it unset on a HEAD", got)
	}
}

// A cross-origin range read has to be able to see which bytes it got. A
// browser hides every response header the server does not name in
// Access-Control-Expose-Headers, so a 206 whose Content-Range is unlisted
// reaches the JS that asked for it looking like a whole-file 200 -- and the
// Web UI sits on a different origin from the API in every deployment shape
// this ships with.
func TestResolve_RangeHeadersAreExposedCrossOrigin(t *testing.T) {
	f := newRangeFixture(t)

	rec := f.do(secRequest{
		method: "GET",
		path:   "/datasets/alice/data/resolve/main/rows.csv",
		headers: map[string]string{
			"Range": "bytes=4-7",
			// The origin newSecFixture allowlists; an unlisted one gets no
			// CORS headers at all (TestCORS_OnlyAllowlistedOriginsGetCredentialedHeaders).
			"Origin": "http://web.test.local",
		},
	})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206; body = %q", rec.Code, rec.Body.String())
	}
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	for _, h := range []string{"Content-Range", "Accept-Ranges", "Content-Length", "ETag"} {
		if !strings.Contains(exposed, h) {
			t.Errorf("Access-Control-Expose-Headers = %q, want it to include %s", exposed, h)
		}
	}
}
