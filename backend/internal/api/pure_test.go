package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// -------------------------------------------------------------- parseRange

func TestParseRange(t *testing.T) {
	const size = int64(1000)
	tests := []struct {
		name        string
		header      string
		wantOffset  int64
		wantLength  int64
		wantVerdict rangeVerdict
	}{
		{"simple range", "bytes=0-99", 0, 100, rangePartial},
		{"open-ended range", "bytes=100-", 100, -1, rangePartial},
		{"suffix range last 50 bytes", "bytes=-50", 950, 50, rangePartial},
		{"suffix range larger than size clamps", "bytes=-5000", 0, size, rangePartial},
		// Past the end is 416, not a whole-body 200: a resumed download that
		// already holds the file would otherwise append a second copy of it.
		{"start beyond size is unsatisfiable", "bytes=1000-1050", 0, -1, rangeUnsatisfiable},
		{"start equal to size is unsatisfiable", "bytes=1000-", 0, -1, rangeUnsatisfiable},
		{"end clamped to size-1", "bytes=990-2000", 990, 10, rangePartial},
		{"multiple ranges rejected", "bytes=0-99,200-299", 0, -1, rangeNone},
		{"missing bytes= prefix", "0-99", 0, -1, rangeNone},
		{"garbage", "not-a-range", 0, -1, rangeNone},
		{"empty header", "", 0, -1, rangeNone},
		{"end before start invalid", "bytes=100-50", 0, -1, rangeNone},
		{"non-numeric start", "bytes=abc-100", 0, -1, rangeNone},
		{"non-numeric end", "bytes=0-abc", 0, -1, rangeNone},
		{"negative start invalid", "bytes=-1-5", 0, -1, rangeNone},
		{"negative suffix length zero invalid", "bytes=-0", 0, -1, rangeNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, length, verdict := parseRange(tt.header, size)
			if verdict != tt.wantVerdict {
				t.Fatalf("parseRange(%q, %d) verdict = %v, want %v (offset=%d length=%d)", tt.header, size, verdict, tt.wantVerdict, offset, length)
			}
			if verdict != rangePartial {
				return
			}
			if offset != tt.wantOffset {
				t.Errorf("parseRange(%q, %d) offset = %d, want %d", tt.header, size, offset, tt.wantOffset)
			}
			if length != tt.wantLength {
				t.Errorf("parseRange(%q, %d) length = %d, want %d", tt.header, size, length, tt.wantLength)
			}
		})
	}
}

// ------------------------------------------------------------- validateName

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"simple-name", false},
		{"name_with_underscores", false},
		{"name.with.dots", false},
		{"UPPERlower123", false},
		{"1starts-with-digit", false},
		{"", true},
		{"-starts-with-dash", true},
		{".starts-with-dot", true},
		{"_starts-with-underscore", true},
		{"has space", true},
		{"has/slash", true},
		{"repo.git", true}, // explicit .git suffix rejection
		{"a", false},       // single char is fine
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestValidateName_LengthBoundary(t *testing.T) {
	// The pattern allows 1-96 characters total.
	ok96 := make([]byte, 96)
	for i := range ok96 {
		ok96[i] = 'a'
	}
	if err := validateName(string(ok96)); err != nil {
		t.Errorf("validateName(96 chars) = %v, want nil", err)
	}

	tooLong := make([]byte, 97)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	if err := validateName(string(tooLong)); err == nil {
		t.Errorf("validateName(97 chars) = nil, want an error")
	}
}

// ------------------------------------------------------------ validRunStatus

func TestValidRunStatus(t *testing.T) {
	valid := []string{"running", "finished", "failed"}
	for _, s := range valid {
		if !validRunStatus(s) {
			t.Errorf("validRunStatus(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "Running", "done", "crashed", "finished ", "running\n"}
	for _, s := range invalid {
		if validRunStatus(s) {
			t.Errorf("validRunStatus(%q) = true, want false", s)
		}
	}
	// The compiler cannot force this switch and the apitypes consts to stay in
	// sync, so pin the contract here too.
	for _, s := range []apitypes.RunStatus{
		apitypes.RunStatusRunning, apitypes.RunStatusFinished, apitypes.RunStatusFailed,
	} {
		if !validRunStatus(string(s)) {
			t.Errorf("validRunStatus(%q) = false for an apitypes const", s)
		}
	}
}

// -------------------------------------------------------------- previewKind

func TestPreviewKind(t *testing.T) {
	tests := []struct {
		path string
		want apitypes.PreviewKind
	}{
		{"data.parquet", "parquet"},
		{"DATA.PARQUET", "parquet"},
		{"README.md", "markdown"},
		{"notes.markdown", "markdown"},
		{"photo.png", "image"},
		{"photo.JPG", "image"},
		{"photo.jpeg", "image"},
		{"anim.gif", "image"},
		{"pic.webp", "image"},
		{"vector.svg", "image"},
		{"notes.txt", "text"},
		{"data.json", "text"},
		{"data.jsonl", "text"},
		{"config.yaml", "text"},
		{"config.yml", "text"},
		{"table.csv", "text"},
		{"table.tsv", "text"},
		{"script.py", "text"},
		{"script.sh", "text"},
		{"pyproject.toml", "text"},
		{"setup.cfg", "text"},
		{"tox.ini", "text"},
		{".gitattributes", "text"},
		{"LICENSE", "text"},
		{"Makefile", "text"},
		{"Dockerfile", "text"},
		{".gitignore", "text"},
		{"src/main.go", "text"},
		{"app.ts", "text"},
		{"model.safetensors", "model"},
		{"weights.bin", "model"},
		{"last.ckpt", "model"},
		{"weights.pt", "model"},
		{"archive.tar.gz", "binary"},
		{"noextension", "binary"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := previewKind(tt.path); got != tt.want {
				t.Errorf("previewKind(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------- decodeJSON

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name       string
		body       string
		limit      int64
		wantOK     bool
		wantStatus int
	}{
		{"accepts a body inside the limit", `{"name":"ok"}`, 1024, true, http.StatusOK},
		{"rejects malformed JSON", `{`, 1024, false, http.StatusBadRequest},
		{"rejects a body over the limit", `{"name":"` + strings.Repeat("x", 200) + `"}`, 32, false, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))

			var got payload
			if ok := decodeJSON(rec, req, tt.limit, &got, "bad body"); ok != tt.wantOK {
				t.Fatalf("decodeJSON ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK && rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// decodeJSON must not echo the decoder's own message, which can name Go types
// and offsets that mean nothing to a client.
func TestDecodeJSONDoesNotEchoDecoderDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":123}`))

	var got struct {
		Name string `json:"name"`
	}
	if decodeJSON(rec, req, 1024, &got, "request body must be JSON with a name") {
		t.Fatal("decodeJSON accepted a body whose field has the wrong type")
	}
	if body := rec.Body.String(); strings.Contains(body, "cannot unmarshal") || strings.Contains(body, "Go struct") {
		t.Fatalf("response leaked decoder detail: %s", body)
	}
}

// --------------------------------------------------------------- listRepoTags

func TestListRepoTags(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"no tags", "", []string{}},
		{"singular tag= only (old links)", "tag=nlp", []string{"nlp"}},
		{"repeated tags= only (facet sidebar)", "tags=nlp&tags=pytorch", []string{"nlp", "pytorch"}},
		{
			"tag= and tags= merge with dedup",
			"tag=nlp&tags=nlp&tags=pytorch",
			[]string{"nlp", "pytorch"},
		},
		{"empty tag= is ignored", "tag=&tags=nlp", []string{"nlp"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tt.query, err)
			}
			if got := listRepoTags(q); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("listRepoTags(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------- experiment ingest names

func TestValidateIngestName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"plain name", "run-1", false},
		{"unicode name", "実験/1", false},
		{"at the size limit", strings.Repeat("a", maxIngestNameBytes), false},
		{"empty", "", true},
		{"over the size limit", strings.Repeat("a", maxIngestNameBytes+1), true},
		{"newline", "run\nname", true},
		{"NUL byte", "run\x00name", true},
		{"invalid UTF-8", "run\xff", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateIngestName(tt.input); (err != nil) != tt.wantErr {
				t.Fatalf("validateIngestName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// ------------------------------------------------------------ run annotations

func TestDecodeRunSegment(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// chi hands back the segment with %2F intact but everything else
		// already decoded, which is what these cases pin down.
		{"plain name is untouched", "run-1", "run-1"},
		{"encoded slash", "sweep%2Frun-1", "sweep/run-1"},
		{"lowercase encoded slash", "sweep%2frun-1", "sweep/run-1"},
		{"several encoded slashes", "a%2Fb%2Fc", "a/b/c"},
		{"an already-decoded space stays decoded", "run 1", "run 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeRunSegment(tt.input); got != tt.want {
				t.Fatalf("decodeRunSegment(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{"trims and keeps the caller's order", []string{" b ", "a"}, []string{"b", "a"}, false},
		{"drops empty and whitespace-only tags", []string{"", "   ", "a"}, []string{"a"}, false},
		{"collapses duplicates", []string{"a", "a", "b"}, []string{"a", "b"}, false},
		{"nil becomes an empty list", nil, []string{}, false},
		{
			"tag at the size limit",
			[]string{strings.Repeat("a", maxRunTagBytes)},
			[]string{strings.Repeat("a", maxRunTagBytes)},
			false,
		},
		{"tag over the size limit", []string{strings.Repeat("a", maxRunTagBytes+1)}, nil, true},
		{"control character", []string{"a\nb"}, nil, true},
		{"invalid UTF-8", []string{"a\xff"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTags(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeTags(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("normalizeTags(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// The count limit gets its own test because building the oversized input in
// the table above would dwarf every other case.
func TestNormalizeTagsRejectsTooMany(t *testing.T) {
	many := make([]string, maxRunTags+1)
	for i := range many {
		many[i] = "tag" + strconv.Itoa(i)
	}
	if _, err := normalizeTags(many); err == nil {
		t.Fatalf("normalizeTags with %d tags: want an error, got nil", len(many))
	}
	if _, err := normalizeTags(many[:maxRunTags]); err != nil {
		t.Fatalf("normalizeTags with %d tags: %v", maxRunTags, err)
	}
}

func TestNormalizeNote(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty clears the note", "", "", false},
		{"markdown is kept verbatim", "# lr sweep\n\n- diverged\n", "# lr sweep\n\n- diverged", false},
		{"tabs and CRLF are allowed", "a\tb\r\nc", "a\tb\r\nc", false},
		{"trailing whitespace trimmed", "note  \n\n\t", "note", false},
		{"control characters rejected", "a\x00b", "", true},
		{"del rejected", "a\x7fb", "", true},
		{"invalid utf-8 rejected", "\xff", "", true},
		{"oversized rejected", strings.Repeat("x", maxRunNoteBytes+1), "", true},
		{"at the limit accepted", strings.Repeat("x", maxRunNoteBytes), strings.Repeat("x", maxRunNoteBytes), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeNote(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeNote error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("normalizeNote = %q, want %q", got, tt.want)
			}
		})
	}
}

// ------------------------------------------------ handler deadline exemptions

// The handler deadline is a bound on requests whose cost the request itself
// describes. Two kinds of route are exempt, and each has to stay exactly as
// wide as its reason: streamingRoute for bodies that are streamed or
// arbitrarily large, cascadeDeleteRoute for the deletes whose cost is the
// amount of data stored (a repository's whole file index and every metric
// point under it) rather than anything in the request. A minute is not a
// bound there, it is a ceiling on how much data may ever be deleted.
func TestCascadeDeleteRoute(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{"DELETE", "/api/v1/repos/models/alice/bert", true},
		{"DELETE", "/api/v1/repos/datasets/alice/squad", true},
		{"DELETE", "/api/repos/delete", true},
		{"DELETE", "/api/v1/experiments/alice/exp/proj/runs/run-1", true},

		// Same prefixes, but a single-row write in every case.
		{"DELETE", "/api/v1/repos/models/alice/bert/archive", false},
		{"DELETE", "/api/v1/repos/models/alice/bert/transfer", false},
		{"DELETE", "/api/v1/experiments/alice/exp/proj/runs/run-1/artifacts", false},
		// The exemption is for the delete, not for the route.
		{"GET", "/api/v1/repos/models/alice/bert", false},
		{"PATCH", "/api/v1/repos/models/alice/bert", false},
		{"POST", "/api/v1/experiments/alice/exp/proj/log", false},
		// Nothing else is exempt on this ground.
		{"DELETE", "/api/v1/tokens/3", false},
		{"DELETE", "/api/v1/webhooks/3", false},
		{"DELETE", "/api/v1/orgs/acme", false},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			if got := cascadeDeleteRoute(r); got != tt.want {
				t.Errorf("cascadeDeleteRoute(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// boundHandlerTime must consult both exemptions, not just the streaming one.
func TestBoundHandlerTime_ExemptsCascadeDeletes(t *testing.T) {
	var timed bool
	h := boundHandlerTime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TimeoutHandler hands the handler a ResponseWriter of its own, so
		// the wrapper is detectable without waiting a minute for it.
		_, plain := w.(*httptest.ResponseRecorder)
		timed = !plain
		w.WriteHeader(http.StatusNoContent)
	}))
	check := func(method, path string, wantTimed bool) {
		t.Helper()
		timed = false
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
		if timed != wantTimed {
			t.Errorf("%s %s: timed = %v, want %v", method, path, timed, wantTimed)
		}
	}
	check("GET", "/api/v1/repos", true)
	check("DELETE", "/api/v1/repos/models/alice/bert", false)
	check("GET", "/alice/bert/resolve/main/config.json", false)
}
