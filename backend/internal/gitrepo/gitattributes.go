package gitrepo

import (
	"path"
	"strings"
)

// KindDataset is the repository kind whose seeded .gitattributes carries the
// media rules below. Spelled out here so this file does not depend on the
// store package for one string.
const KindDataset = "dataset"

// DefaultGitAttributes returns the .gitattributes seeded into a new repository
// of the given kind, so that large binary formats go to LFS from the first
// push. It is also the fallback used when reading a repository's own
// .gitattributes fails, which is why every caller must pass the kind rather
// than assume one: routing a dataset's audio files as ordinary blobs because
// the fallback was the model list would put megabytes of WAV into the object
// database.
func DefaultGitAttributes(kind string) string {
	if kind == KindDataset {
		return commonGitAttributes + datasetGitAttributes
	}
	return commonGitAttributes
}

// commonGitAttributes matches what a HuggingFace repository ships with (plus
// the GGUF formats llama.cpp made ubiquitous after that list was written), so
// a repository cloned from either hub routes files the same way.
const commonGitAttributes = `*.7z filter=lfs diff=lfs merge=lfs -text
*.arrow filter=lfs diff=lfs merge=lfs -text
*.bin filter=lfs diff=lfs merge=lfs -text
*.bz2 filter=lfs diff=lfs merge=lfs -text
*.ckpt filter=lfs diff=lfs merge=lfs -text
*.ftz filter=lfs diff=lfs merge=lfs -text
*.gz filter=lfs diff=lfs merge=lfs -text
*.h5 filter=lfs diff=lfs merge=lfs -text
*.joblib filter=lfs diff=lfs merge=lfs -text
*.lfs.* filter=lfs diff=lfs merge=lfs -text
*.mlmodel filter=lfs diff=lfs merge=lfs -text
*.model filter=lfs diff=lfs merge=lfs -text
*.msgpack filter=lfs diff=lfs merge=lfs -text
*.npy filter=lfs diff=lfs merge=lfs -text
*.npz filter=lfs diff=lfs merge=lfs -text
*.onnx filter=lfs diff=lfs merge=lfs -text
*.ot filter=lfs diff=lfs merge=lfs -text
*.parquet filter=lfs diff=lfs merge=lfs -text
*.pb filter=lfs diff=lfs merge=lfs -text
*.pickle filter=lfs diff=lfs merge=lfs -text
*.pkl filter=lfs diff=lfs merge=lfs -text
*.pt filter=lfs diff=lfs merge=lfs -text
*.pth filter=lfs diff=lfs merge=lfs -text
*.rar filter=lfs diff=lfs merge=lfs -text
*.safetensors filter=lfs diff=lfs merge=lfs -text
saved_model/**/* filter=lfs diff=lfs merge=lfs -text
*.tar filter=lfs diff=lfs merge=lfs -text
*.tar.* filter=lfs diff=lfs merge=lfs -text
*.tflite filter=lfs diff=lfs merge=lfs -text
*.tgz filter=lfs diff=lfs merge=lfs -text
*.wasm filter=lfs diff=lfs merge=lfs -text
*.xz filter=lfs diff=lfs merge=lfs -text
*.zip filter=lfs diff=lfs merge=lfs -text
*.zst filter=lfs diff=lfs merge=lfs -text
*.gguf filter=lfs diff=lfs merge=lfs -text
*.ggml filter=lfs diff=lfs merge=lfs -text
*tfevents* filter=lfs diff=lfs merge=lfs -text
`

// datasetGitAttributes is appended for dataset repositories only, matching how
// HuggingFace splits its two templates. Media files are the payload of a
// dataset and belong in LFS however small any single one is; in a model
// repository the same patterns would only push the screenshots in a model card
// through an LFS round trip.
const datasetGitAttributes = `# Audio - uncompressed
*.pcm filter=lfs diff=lfs merge=lfs -text
*.sam filter=lfs diff=lfs merge=lfs -text
*.raw filter=lfs diff=lfs merge=lfs -text
# Audio - compressed
*.aac filter=lfs diff=lfs merge=lfs -text
*.flac filter=lfs diff=lfs merge=lfs -text
*.mp3 filter=lfs diff=lfs merge=lfs -text
*.ogg filter=lfs diff=lfs merge=lfs -text
*.wav filter=lfs diff=lfs merge=lfs -text
# Image - uncompressed
*.bmp filter=lfs diff=lfs merge=lfs -text
*.gif filter=lfs diff=lfs merge=lfs -text
*.png filter=lfs diff=lfs merge=lfs -text
*.tiff filter=lfs diff=lfs merge=lfs -text
# Image - compressed
*.jpg filter=lfs diff=lfs merge=lfs -text
*.jpeg filter=lfs diff=lfs merge=lfs -text
*.webp filter=lfs diff=lfs merge=lfs -text
# Video
*.avi filter=lfs diff=lfs merge=lfs -text
*.mkv filter=lfs diff=lfs merge=lfs -text
*.mov filter=lfs diff=lfs merge=lfs -text
*.mp4 filter=lfs diff=lfs merge=lfs -text
*.webm filter=lfs diff=lfs merge=lfs -text
# Packed and embedded datasets
*.db filter=lfs diff=lfs merge=lfs -text
*.duckdb filter=lfs diff=lfs merge=lfs -text
*.lz4 filter=lfs diff=lfs merge=lfs -text
*.mds filter=lfs diff=lfs merge=lfs -text
*.sqlite filter=lfs diff=lfs merge=lfs -text
*.sqlite3 filter=lfs diff=lfs merge=lfs -text
`

// LFSInlineThreshold is the size above which a file goes to LFS even when no
// .gitattributes pattern matches it. Keeping ordinary blobs small is what
// keeps the bare repositories cheap to clone.
const LFSInlineThreshold = 10 << 20 // 10 MiB

// LFSRules is the subset of .gitattributes that decides LFS routing.
type LFSRules struct {
	patterns []lfsRule
}

type lfsRule struct {
	pattern string
	lfs     bool
}

// ParseGitAttributes extracts the LFS filter rules from a .gitattributes file.
// Later rules win, which is git's own precedence.
//
// Only one file is parsed: the caller reads the .gitattributes at the root of
// the revision and nothing else. git also honours a .gitattributes in every
// directory, applied to that subtree and taking precedence over the root's, so
// a repository that puts its rules in `data/.gitattributes` is routed here as
// if those rules did not exist -- while the contributor's own git-lfs obeys
// them on push, which is how the same file ends up stored two different ways
// depending on which client wrote it. Fixing that means reading a
// .gitattributes per directory and matching against the nearest one, which is
// a change to how callers load rules (internal/api/commit.go's loadLFSRules,
// internal/experiments/flush.go) rather than to this function.
func ParseGitAttributes(content []byte) *LFSRules {
	r := &LFSRules{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pattern, attrs, ok := splitAttrLine(line)
		if !ok {
			continue
		}
		for _, attr := range attrs {
			if isFilter, lfs := parseFilterAttr(attr); isFilter {
				r.patterns = append(r.patterns, lfsRule{pattern: pattern, lfs: lfs})
			}
		}
	}
	return r
}

// splitAttrLine separates a .gitattributes line into its pattern and its
// attribute tokens.
//
// The pattern is double-quoted whenever it contains a space (git quotes it on
// write and every git-lfs-generated file does the same), so splitting the
// whole line on whitespace loses those lines entirely: `"my data.bin"
// filter=lfs` came apart into the pattern `"my` and an attribute `data.bin"`,
// and the rule vanished.
func splitAttrLine(line string) (pattern string, attrs []string, ok bool) {
	var rest string
	if strings.HasPrefix(line, `"`) {
		pattern, rest, ok = unquoteAttrPattern(line)
		if !ok {
			return "", nil, false
		}
	} else {
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			return "", nil, false
		}
		pattern, rest = line[:i], line[i:]
	}
	attrs = strings.Fields(rest)
	if pattern == "" || len(attrs) == 0 {
		return "", nil, false
	}
	return pattern, attrs, true
}

// unquoteAttrPattern reads the leading double-quoted pattern off a line and
// returns it along with whatever follows the closing quote. git writes these
// with the same C-style escapes as quote_c_style/unquote_c_style in git's own
// quote.c: \" and \\, the named control escapes (\a \b \f \n \r \t \v), and a
// \NNN octal byte for anything else -- the form core.quotePath's default
// makes git fall back to for a non-ASCII byte in a path, so a pattern over a
// file like "café.bin" is written as "caf\303\251.bin". Any other escape is
// taken as the character it precedes rather than rejected -- guessing wrong
// on a truly exotic one costs a match, while refusing the line costs the
// whole rule.
func unquoteAttrPattern(line string) (pattern, rest string, ok bool) {
	var b strings.Builder
	i := 1
	for i < len(line) {
		c := line[i]
		if c == '"' {
			return b.String(), line[i+1:], true
		}
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(line) {
			return "", "", false
		}
		e := line[i]
		switch e {
		case 'a':
			b.WriteByte('\a')
			i++
		case 'b':
			b.WriteByte('\b')
			i++
		case 'f':
			b.WriteByte('\f')
			i++
		case 'n':
			b.WriteByte('\n')
			i++
		case 'r':
			b.WriteByte('\r')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case 'v':
			b.WriteByte('\v')
			i++
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// git always emits exactly three octal digits per escaped byte;
			// read up to three here too, leniently, rather than demanding it.
			n, digits := 0, 0
			for digits < 3 && i < len(line) && line[i] >= '0' && line[i] <= '7' {
				n = n*8 + int(line[i]-'0')
				i++
				digits++
			}
			b.WriteByte(byte(n))
		default:
			b.WriteByte(e)
			i++
		}
	}
	// Unterminated: not something git would have written, and there is no
	// sensible pattern to guess at.
	return "", "", false
}

// parseFilterAttr decides what one attribute token says about the filter
// attribute, following git's own parser (attr.c): the leading `-` (unset) or
// `!` (unspecified) sign is stripped before the `=` is looked at, so a value
// on a negated token is discarded rather than becoming part of the name.
//
// isFilter is false for every attribute that is not `filter`, including the
// `diff=lfs -text` that keeps it company on a git-lfs line.
//
// What this replaced recognised `filter=lfs`, `-filter=lfs` and `filter=` and
// nothing else -- so the two spellings git-lfs's own documentation uses to
// take a path back out of LFS, `-filter` and `!filter`, both read as "no rule
// here" and the earlier `*.bin filter=lfs` went on winning. The file then went
// to LFS through the upload API while the contributor's git-lfs, which obeyed
// the exclusion, pushed it as a plain blob.
func parseFilterAttr(tok string) (isFilter, lfs bool) {
	if tok == "" {
		return false, false
	}
	name, value := tok, ""
	negated := tok[0] == '-' || tok[0] == '!'
	if negated {
		name = tok[1:]
	}
	if i := strings.IndexByte(name, '='); i >= 0 {
		name, value = name[:i], name[i+1:]
	}
	if name != "filter" {
		return false, false
	}
	if negated {
		// Unset and unspecified both mean no filter driver runs, so the path
		// is an ordinary blob however large it is.
		return true, false
	}
	// A bare `filter` sets the attribute to true rather than to a driver
	// name, and `filter=clean-something-else` names another driver; only
	// `filter=lfs` is LFS.
	return true, value == "lfs"
}

// ShouldUseLFS decides how a file is stored. Explicit .gitattributes rules take
// precedence; otherwise anything at or above LFSInlineThreshold goes to LFS.
func (r *LFSRules) ShouldUseLFS(filePath string, size int64) bool {
	if r != nil {
		matched, lfs := false, false
		for _, rule := range r.patterns {
			if matchAttrPattern(rule.pattern, filePath) {
				matched, lfs = true, rule.lfs
			}
		}
		if matched {
			return lfs
		}
	}
	return size >= LFSInlineThreshold
}

// matchAttrPattern implements the slice of gitignore-style matching that
// .gitattributes files actually use in practice: a bare glob matches the base
// name at any depth, while a pattern containing a slash is anchored to the
// repository root.
func matchAttrPattern(pattern, filePath string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(filePath))
		return err == nil && ok
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(filePath, "/"))
}

// matchSegments matches an anchored pattern segment by segment, with "**"
// standing for zero or more segments -- which covers all three shapes git
// gives it: a leading "**/" (match in any directory), a trailing "/**" (match
// everything below), and "/**/" in the middle (match across any depth).
//
// path.Match alone cannot do this: its "*" never crosses a separator, so it
// reads "**" as one more single segment and quietly matches only at the one
// depth. That mattered as soon as the seeded list gained saved_model/**/*, and
// it matters more for hand-written rules -- a user's `data/**/*.bin` is
// honoured by their git-lfs on push but was ignored by this server on upload,
// so the same file took different routes depending on the client.
//
// The matching itself is a table rather than the obvious recursion. Both the
// pattern and the path come from untrusted input -- the pattern from the
// repository's own .gitattributes, the path from the request body -- and every
// preupload weighs each of a request's files against every rule. Backtracking
// over each "**" independently makes that O(n^k) for k stars over an n-segment
// path, and a hand-written file with eight "**" against a deep path took
// seconds per path to answer "no". Filling a table instead visits each
// (pattern, name) pair once, so the same input is linear in their product.
func matchSegments(pattern, name []string) bool {
	pattern = collapseDoubleStars(pattern)
	m, n := len(pattern), len(name)

	// row[j] answers "does pattern[i:] match name[j:]?" for the i currently
	// being filled. Rows are computed from the end backwards, and each one
	// only ever reads the row after it and its own cell to the right, so two
	// rows are enough -- the full m*n table would be sized by two untrusted
	// inputs at once.
	next := make([]bool, n+1) // the row for pattern[m:], i.e. the empty pattern
	cur := make([]bool, n+1)
	next[n] = true // both exhausted together: a match

	for i := m - 1; i >= 0; i-- {
		switch {
		case pattern[i] == "**" && i == m-1:
			// A trailing "/**" matches everything *inside*, so there has to
			// be something left to be inside of.
			for j := 0; j < n; j++ {
				cur[j] = true
			}
			cur[n] = false
		case pattern[i] == "**":
			// "**" stands for zero or more segments: either it consumes
			// nothing here, or it swallows name[j] and asks the same question
			// one segment along. Written this way the whole star costs one
			// row instead of a fan-out over every split point.
			cur[n] = next[n]
			for j := n - 1; j >= 0; j-- {
				cur[j] = next[j] || cur[j+1]
			}
		default:
			cur[n] = false
			for j := n - 1; j >= 0; j-- {
				if !next[j+1] {
					cur[j] = false
					continue
				}
				// A malformed pattern segment counts as "does not match"
				// rather than aborting: the recursion this replaced reached
				// the same answer, since a segment that can never match fails
				// at every alignment anyway.
				ok, err := path.Match(pattern[i], name[j])
				cur[j] = err == nil && ok
			}
		}
		cur, next = next, cur
	}
	return next[0]
}

// collapseDoubleStars folds runs of "**" into one. Consecutive stars mean
// exactly what a single one means -- zero or more segments -- so dropping the
// extras costs nothing semantically and keeps a pattern of nothing but stars
// from widening the table for no reason.
func collapseDoubleStars(pattern []string) []string {
	out := make([]string, 0, len(pattern))
	for _, seg := range pattern {
		if seg == "**" && len(out) > 0 && out[len(out)-1] == "**" {
			continue
		}
		out = append(out, seg)
	}
	return out
}
