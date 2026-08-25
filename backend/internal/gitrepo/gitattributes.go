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
func ParseGitAttributes(content []byte) *LFSRules {
	r := &LFSRules{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pattern := fields[0]
		for _, attr := range fields[1:] {
			switch attr {
			case "filter=lfs":
				r.patterns = append(r.patterns, lfsRule{pattern: pattern, lfs: true})
			case "-filter=lfs", "filter=":
				r.patterns = append(r.patterns, lfsRule{pattern: pattern, lfs: false})
			}
		}
	}
	return r
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
