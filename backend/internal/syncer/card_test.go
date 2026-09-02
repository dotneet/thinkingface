package syncer

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

const cardFrontMatter = `---
license: mit
pipeline_tag: text-generation
tags:
  - nlp
base_model: alice/base
---
`

func (f *pushFixture) repoRow(t *testing.T) *store.Repo {
	t.Helper()
	r, err := f.st.GetRepoByID(f.ctx, f.repo.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	return r
}

func (f *pushFixture) lineageRaws(t *testing.T) []string {
	t.Helper()
	edges, err := f.st.ListRepoLineage(f.ctx, f.repo.ID)
	if err != nil {
		t.Fatalf("list lineage: %v", err)
	}
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.Raw)
	}
	return out
}

// A README that grows past the indexer's read limit used to erase everything
// derived from it. ReadFile refuses an oversized blob with ErrBlobTooLarge --
// deliberately distinct from "missing" -- and the syncer treated the two
// alike, so the empty card it fell back to went straight into UpdateRepoIndex
// and ReplaceRepoLineage: license, tags, pipeline_tag and every lineage edge
// gone, on a push whose front matter had not changed by a byte. Model cards
// with benchmark tables cross 256 KiB routinely.
//
// The front matter is at the top of the file, so it is now read from a prefix
// and the card survives intact.
func TestPush_LargeReadmeStillIndexesItsCard(t *testing.T) {
	f := newPushFixture(t)

	f.push("main", addOp("README.md", cardFrontMatter+"# small\n"))
	before := f.repoRow(t)
	if before.Card["license"] != "mit" {
		t.Fatalf("card = %#v, want the front matter indexed", before.Card)
	}

	// Same front matter, a body far past the read limit.
	body := strings.Repeat("| bench | 0.9 |\n", 30000)
	if len(body) < maxReadmeBytes {
		t.Fatalf("test body is %d bytes, which does not exceed the %d byte limit", len(body), maxReadmeBytes)
	}
	f.push("main", addOp("README.md", cardFrontMatter+body))

	after := f.repoRow(t)
	if !reflect.DeepEqual(after.Card, before.Card) {
		t.Errorf("card after the large push = %#v, want it unchanged from %#v", after.Card, before.Card)
	}
	if raws := f.lineageRaws(t); len(raws) != 1 || raws[0] != "alice/base" {
		t.Errorf("lineage = %v, want the base_model edge kept", raws)
	}
	if after.HeadSHA == before.HeadSHA {
		t.Error("head_sha did not advance; the push itself must still be recorded")
	}
}

// The one genuinely unreadable case: front matter that opens inside the read
// limit and does not close inside it. Nothing can be known about the card, so
// the previous one is kept rather than replaced with an empty one -- and the
// push is still recorded.
func TestPush_UnreadableReadmeKeepsTheExistingCard(t *testing.T) {
	f := newPushFixture(t)

	f.push("main", addOp("README.md", cardFrontMatter+"# small\n"))
	before := f.repoRow(t)

	var huge strings.Builder
	huge.WriteString("---\nlicense: mit\n")
	for i := 0; huge.Len() < maxReadmeBytes+1024; i++ {
		fmt.Fprintf(&huge, "filler_%d: %s\n", i, strings.Repeat("x", 200))
	}
	huge.WriteString("---\nbody\n")
	f.push("main", addOp("README.md", huge.String()))

	after := f.repoRow(t)
	if !reflect.DeepEqual(after.Card, before.Card) {
		t.Errorf("card = %#v, want the previous card kept rather than erased", after.Card)
	}
	if after.Description != before.Description {
		t.Errorf("description = %q, want %q", after.Description, before.Description)
	}
	if raws := f.lineageRaws(t); len(raws) != 1 || raws[0] != "alice/base" {
		t.Errorf("lineage = %v, want the previous edges kept", raws)
	}
	if after.HeadSHA == before.HeadSHA {
		t.Error("head_sha did not advance; an unreadable README must not stall the index")
	}
}

// The fallback must not become "the card can never be cleared": a README that
// really was deleted, or really did lose its front matter, still empties the
// index.
func TestPush_DeletedReadmeClearsTheCard(t *testing.T) {
	f := newPushFixture(t)

	f.push("main", addOp("README.md", cardFrontMatter+"# small\n"))
	f.push("main", addOp("README.md", "# no front matter any more\n"))

	after := f.repoRow(t)
	if len(after.Card) != 0 {
		t.Errorf("card = %#v, want it cleared", after.Card)
	}
	if raws := f.lineageRaws(t); len(raws) != 0 {
		t.Errorf("lineage = %v, want the edges dropped with the front matter", raws)
	}
}

func TestFrontMatterUnclosed(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"no front matter", "# just a heading\n", false},
		{"closed", "---\nlicense: mit\n---\nbody\n", false},
		{"closed with crlf", "---\r\nlicense: mit\r\n---\r\nbody\r\n", false},
		{"open", "---\nlicense: mit\n", true},
		{"empty", "", false},
		{"delimiter only", "---\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := frontMatterUnclosed([]byte(tt.in)); got != tt.want {
				t.Fatalf("frontMatterUnclosed(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
