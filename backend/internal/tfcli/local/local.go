// Package local is the filesystem half of `tf up`: it walks the directory (or
// single file) the user named, applies include/exclude globs, infers whether
// the content looks like a model or a dataset, and derives a repository name
// from the path. It knows nothing about HTTP.
package local

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Options filters the walk.
type Options struct {
	// Include keeps only paths matching at least one pattern (empty = all).
	Include []string
	// Exclude drops paths matching any pattern; applied after Include.
	Exclude []string
	// Hidden uploads dot-files and dot-directories found inside the tree,
	// which are left out by default (see Scan). It does not affect a root
	// the user named explicitly, which is always scanned.
	Hidden bool
}

// File is one file found by Scan.
type File struct {
	RepoPath string // forward-slash path relative to the root ("data/train.parquet")
	AbsPath  string // absolute path on disk
	Size     int64
}

// Open returns a fresh reader for the file (os.Open).
func (f File) Open() (io.ReadCloser, error) {
	return os.Open(f.AbsPath)
}

// Skip reasons reported by Scan. They double as the human wording of the
// warning `tf up` prints, so they read as a sentence fragment after the path.
const (
	// ReasonSymlinkDir: a symlink inside the tree pointing at a directory.
	// It is not followed (that is how a link back up the tree would turn
	// into an unbounded, looping walk), so nothing below it is uploaded.
	ReasonSymlinkDir = "symlink to a directory"
	// ReasonBrokenSymlink: a symlink whose target does not resolve.
	ReasonBrokenSymlink = "broken symlink"
	// ReasonIrregular: a socket, fifo, device node -- something with no
	// byte content to commit.
	ReasonIrregular = "not a regular file"
	// ReasonIgnoredDir: ".git" / "__pycache__", skipped by design and
	// documented, so not worth telling the user about.
	ReasonIgnoredDir = "ignored directory"
	// ReasonHidden: a dot-file or dot-directory inside the tree. Left out
	// by default because repositories here are world-readable and a
	// project directory routinely holds ".env", ".envrc" or ".aws/" next
	// to the data the user meant to publish; --hidden opts back in.
	ReasonHidden = "hidden"
)

// alwaysKeptDotfiles are the dot-files that are repository content rather
// than local machine state, so the hidden rule does not apply to them.
// ".gitattributes" in particular carries the LFS routing rules and must keep
// travelling with the upload.
var alwaysKeptDotfiles = map[string]bool{
	".gitattributes": true,
	".gitignore":     true,
}

// isHidden reports whether a path component is a dot-name (".env", ".venv"),
// excluding "." and ".." which a walk never yields as an entry name anyway.
func isHidden(name string) bool {
	return len(name) > 1 && name[0] == '.' && name != ".."
}

// Skipped is one path Scan deliberately left out of files. Dir means the
// whole subtree beneath RepoPath went unread, so the walk cannot say what (if
// anything) lives down there -- which is precisely why `tf up --delete` must
// keep its hands off those remote paths.
type Skipped struct {
	RepoPath string // forward-slash path relative to the root
	Dir      bool   // the subtree below RepoPath was never enumerated
	Reason   string // one of the Reason* constants above
}

// Notable reports whether the skip is worth showing the user. The routine,
// documented exclusions (.git, __pycache__) are not; anything that drops
// content the user plausibly meant to upload is.
func (s Skipped) Notable() bool { return s.Reason != ReasonIgnoredDir }

// SkippedDirs returns the repo-relative directories in skipped whose contents
// were never enumerated. Callers hand these to hub.Plan.LocalUnknownDirs so a
// delete pass treats "we did not look" differently from "it is gone".
func SkippedDirs(skipped []Skipped) []string {
	var dirs []string
	for _, s := range skipped {
		if s.Dir {
			dirs = append(dirs, s.RepoPath)
		}
	}
	return dirs
}

// Scan walks root. When root is a directory every regular file below it is
// returned with RepoPath relative to root; when root is a single file, one
// entry with RepoPath = its base name. If root itself is a symlink to a
// directory, it is followed and its contents are scanned as normal. Always
// skipped: ".git" directories, ".DS_Store", "Thumbs.db", "__pycache__"
// directories, symlinks *inside* the tree that point at directories (to
// avoid loops; symlinked files are followed), broken symlinks and
// non-regular files. Dot-files and dot-directories found *inside* the tree
// are skipped as well unless opts.Hidden is set -- ".env" and friends are
// local machine state, and a repository here is world-readable -- except for
// the names in alwaysKeptDotfiles, which are repository content. A root the
// user named explicitly is always scanned, hidden or not. Results are sorted
// by RepoPath. A root that does not exist is an error.
//
// allPaths reports every path the walk found on disk -- including the ones
// left out of files, whether by the always-skipped names above or by
// Include/Exclude. Callers that need to know what actually exists on disk, as
// opposed to what this run uploads, use it instead of re-scanning. `tf up
// --delete` is the motivating case: a file the walk skipped is still a file
// that exists, and must never be mistaken for "gone locally" and deleted from
// the remote.
//
// skipped names what did not make it into files and why. Its Dir entries are
// the walk's blind spots -- directories whose contents allPaths therefore
// cannot list -- and its Notable entries are the ones a caller should show
// the user, since a silently unread symlinked directory otherwise looks like
// an upload that simply had nothing to do. A single walk produces all three
// results so scanning a large tree twice is never needed.
func Scan(root string, opts Options) (files []File, allPaths []string, skipped []Skipped, err error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("local: scan %s: %w", root, err)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("local: scan %s: %w", root, err)
	}

	if !info.IsDir() {
		f := File{
			RepoPath: filepath.ToSlash(filepath.Base(rootAbs)),
			AbsPath:  rootAbs,
			Size:     info.Size(),
		}
		allPaths = append(allPaths, f.RepoPath)
		if matchFilters(f.RepoPath, opts) {
			files = append(files, f)
		}
		return files, allPaths, nil, nil
	}

	// root itself may be a symlink to a directory (or sit behind one via an
	// intermediate path component). os.Stat above follows symlinks, so
	// info.IsDir() is already correct, but filepath.Abs only makes the path
	// absolute -- it does not resolve symlinks -- and filepath.WalkDir
	// classifies its *starting* path with os.Lstat rather than os.Stat. Left
	// unresolved, WalkDir's Lstat would see the symlink itself (not a
	// directory), and the `p == rootAbs` check below would return
	// immediately without ever recursing, silently skipping every file
	// underneath. Resolve to the real path so WalkDir's own Lstat agrees
	// with the os.Stat check above. This only changes what rootAbs points
	// to, not how RepoPath is derived (still Rel(rootAbs, p) inside the same
	// walk) or how symlinks *inside* the tree are handled below. os.Stat
	// already proved the path resolves, so a failure here would only be a
	// race; fall back to the unresolved path rather than surface a new,
	// more confusing error.
	if resolved, evalErr := filepath.EvalSymlinks(rootAbs); evalErr == nil {
		rootAbs = resolved
	}

	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == rootAbs {
			return nil
		}
		name := d.Name()
		rel, relErr := filepath.Rel(rootAbs, p)
		if relErr != nil {
			return relErr
		}
		repoPath := filepath.ToSlash(rel)

		if d.IsDir() {
			if name == ".git" || name == "__pycache__" {
				skipped = append(skipped, Skipped{RepoPath: repoPath, Dir: true, Reason: ReasonIgnoredDir})
				return filepath.SkipDir
			}
			if !opts.Hidden && isHidden(name) {
				// Dir: true, so `tf up --delete` treats everything the
				// remote holds under this path as "not looked at"
				// rather than "gone locally".
				skipped = append(skipped, Skipped{RepoPath: repoPath, Dir: true, Reason: ReasonHidden})
				return filepath.SkipDir
			}
			return nil
		}

		// Everything below is a path that exists on disk, so it joins
		// allPaths whether or not it is uploaded: a skipped file is still
		// a file that is *there*, and treating it as absent is how
		// --delete would remove a remote copy of it.
		allPaths = append(allPaths, repoPath)
		keep := func(size int64) {
			if matchFilters(repoPath, opts) {
				files = append(files, File{RepoPath: repoPath, AbsPath: p, Size: size})
			}
		}

		if d.Type()&os.ModeSymlink != 0 {
			target, statErr := os.Stat(p)
			if statErr != nil {
				// Broken symlink: nothing to upload.
				skipped = append(skipped, Skipped{RepoPath: repoPath, Reason: ReasonBrokenSymlink})
				return nil
			}
			if target.IsDir() {
				// Do not follow symlinked directories (avoids cycles).
				// Nothing beneath it was read, so it is a blind spot.
				skipped = append(skipped, Skipped{RepoPath: repoPath, Dir: true, Reason: ReasonSymlinkDir})
				return nil
			}
			if name == ".DS_Store" || name == "Thumbs.db" {
				return nil
			}
			if hiddenSkip(name, opts) {
				skipped = append(skipped, Skipped{RepoPath: repoPath, Reason: ReasonHidden})
				return nil
			}
			if !target.Mode().IsRegular() {
				// A symlink whose target is a FIFO, device node, or socket
				// has no byte content to commit -- the same reason the
				// non-symlink branch below skips one directly. Without this
				// check keep() would report the target's Size() (often 0,
				// as for /dev/zero or a FIFO) and the file would be treated
				// as an ordinary empty-or-small file, so a later os.Open +
				// io.Copy on it hangs instead of failing.
				skipped = append(skipped, Skipped{RepoPath: repoPath, Reason: ReasonIrregular})
				return nil
			}
			keep(target.Size())
			return nil
		}

		if name == ".DS_Store" || name == "Thumbs.db" {
			return nil
		}
		if hiddenSkip(name, opts) {
			// Still in allPaths above: the file exists on disk, so
			// --delete must not read its absence from the upload as
			// "deleted locally" and remove the remote copy.
			skipped = append(skipped, Skipped{RepoPath: repoPath, Reason: ReasonHidden})
			return nil
		}
		if !d.Type().IsRegular() {
			skipped = append(skipped, Skipped{RepoPath: repoPath, Reason: ReasonIrregular})
			return nil
		}

		entryInfo, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		keep(entryInfo.Size())
		return nil
	})
	if walkErr != nil {
		return nil, nil, nil, walkErr
	}

	sort.Slice(files, func(i, j int) bool { return files[i].RepoPath < files[j].RepoPath })
	sort.Strings(allPaths)
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].RepoPath < skipped[j].RepoPath })
	return files, allPaths, skipped, nil
}

// hiddenSkip reports whether a file named name inside the tree is left out
// for being hidden.
func hiddenSkip(name string, opts Options) bool {
	return !opts.Hidden && isHidden(name) && !alwaysKeptDotfiles[name]
}

func matchFilters(repoPath string, opts Options) bool {
	if len(opts.Include) > 0 {
		included := false
		for _, pat := range opts.Include {
			if Match(pat, repoPath) {
				included = true
				break
			}
		}
		if !included {
			return false
		}
	}
	for _, pat := range opts.Exclude {
		if Match(pat, repoPath) {
			return false
		}
	}
	return true
}

// Match reports whether pattern matches repoPath. Patterns are shell globs:
// "*" and "?" are wildcards, "[...]" is a bracket expression ("[ab].csv",
// "[a-z]*", "[!0-9]*" -- "!" or "^" negates it), and "**" matches any number
// of path segments (so "**/*.parquet" and "data/**" work); a pattern with no
// "/" is also tried against the base name ("*.parquet" matches
// "data/train.parquet"). A pattern that names a directory ("data" or
// "data/") matches everything beneath it.
func Match(pattern, repoPath string) bool {
	trimmed := strings.TrimSuffix(pattern, "/")
	if trimmed == "" {
		return false
	}
	repoPath = strings.Trim(filepath.ToSlash(repoPath), "/")

	if globRegexp(trimmed).MatchString(repoPath) {
		return true
	}
	// A pattern that names a directory matches everything beneath it.
	if globRegexp(trimmed + "/**").MatchString(repoPath) {
		return true
	}
	if !strings.Contains(trimmed, "/") {
		if globRegexp(trimmed).MatchString(path.Base(repoPath)) {
			return true
		}
	}
	return false
}

var globRegexpCache sync.Map // pattern string -> *regexp.Regexp

// globRegexp compiles pattern (a "/"-segmented glob, "**" allowed) into an
// anchored regular expression matching a full forward-slash path.
func globRegexp(pattern string) *regexp.Regexp {
	if v, ok := globRegexpCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	re, err := regexp.Compile(globToRegexpSource(pattern))
	if err != nil {
		// globToRegexpSource is built to always emit syntactically valid
		// regexp source for any glob text (see segmentToRegexpSource /
		// parseBracketClass). This is a last-resort guard in case some glob
		// this function did not anticipate still slips through: degrade to
		// a literal, exact-path match rather than let regexp.MustCompile
		// panic and take down the whole `tf` invocation over a malformed
		// --include/--exclude pattern.
		re = regexp.MustCompile("^" + regexp.QuoteMeta(pattern) + "$")
	}
	globRegexpCache.Store(pattern, re)
	return re
}

func globToRegexpSource(pattern string) string {
	segs := strings.Split(pattern, "/")

	var b strings.Builder
	b.WriteString("^")
	consumedSeparator := false // previous segment already accounted for the following "/"
	for i := 0; i < len(segs); i++ {
		seg := segs[i]
		if seg == "**" {
			// Collapse a run of consecutive "**" segments into one.
			j := i
			for j < len(segs) && segs[j] == "**" {
				j++
			}
			isFirst := i == 0
			isLast := j == len(segs)
			switch {
			case isFirst && isLast:
				// The whole pattern is "**": matches anything.
				b.WriteString(".*")
			case isFirst:
				// Leading "**/": zero or more whole path segments.
				b.WriteString("(?:.*/)?")
				consumedSeparator = true
			case isLast:
				// Trailing "/**" after real segments: needs the boundary
				// slash then anything.
				if !consumedSeparator {
					b.WriteString("/")
				}
				b.WriteString(".*")
				consumedSeparator = false
			default:
				// Middle "**": zero or more whole path segments.
				if !consumedSeparator {
					b.WriteString("/")
				}
				b.WriteString("(?:.*/)?")
				consumedSeparator = true
			}
			i = j - 1
			continue
		}

		if i > 0 && !consumedSeparator {
			b.WriteString("/")
		}
		b.WriteString(segmentToRegexpSource(seg))
		consumedSeparator = false
	}
	b.WriteString("$")
	return b.String()
}

// segmentToRegexpSource converts one "/"-free glob segment into a regexp
// source fragment: "*" and "?" are wildcards, "[...]" is a shell-style
// bracket expression (a character class, "!" or "^" negating it, e.g.
// "[ab].csv" or "[!0-9]*"), and everything else -- including a "[" that never
// finds a matching "]" -- is a literal, with regexp metacharacters escaped.
func segmentToRegexpSource(seg string) string {
	var b strings.Builder
	runes := []rune(seg)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			if class, end, ok := parseBracketClass(runes, i); ok {
				b.WriteString(class)
				i = end
				continue
			}
			b.WriteString(`\[`)
		case '.', '+', '(', ')', '|', ']', '{', '}', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// slashRune is '/' as a rune -- the code point a bracket class must never
// match (see parseBracketClass), kept named since the range-splitting logic
// below reasons about the code points immediately on either side of it.
const slashRune = '/'

// neverMatchClass is a regexp character class that cannot match any rune. Go's
// regexp syntax has no literal "matches nothing" atom and rejects an empty
// "[]", so this stands in for the degenerate case of a positive bracket class
// whose only member(s) were '/' -- which a shell/gitignore glob's bracket
// class can never match, leaving nothing behind to match at all.
const neverMatchClass = `[^\x{0}-\x{10FFFF}]`

// parseBracketClass reads a shell-style bracket expression starting at
// runes[i] (which must be '['), following the usual glob conventions: "!" or
// "^" right after "[" negates the class, and a "]" that would otherwise close
// an empty class is instead its first literal member (so "[]ab]" matches "]",
// "a" or "b"). Per shell/gitignore glob semantics, a bracket class -- negated
// or not -- never matches '/': "*" and "?" are already compiled to exclude it
// (see segmentToRegexpSource), and a class must follow the same rule, so a
// negated class always excludes '/' even when it was never listed, and a
// literal member or range that would otherwise cover '/' has it carved back
// out. ok is false when there is no matching "]" at all, in which case "[" is
// not a class and the caller must treat it as a literal character. On
// success, class is a regexp fragment (a "[...]" class, or neverMatchClass)
// and end is the index of the closing "]" in runes.
func parseBracketClass(runes []rune, i int) (class string, end int, ok bool) {
	n := len(runes)
	j := i + 1
	var negate bool
	if j < n && (runes[j] == '!' || runes[j] == '^') {
		negate = true
		j++
	}
	start := j
	if j < n && runes[j] == ']' {
		j++ // a leading "]" is a literal member, not the closing delimiter
	}
	for j < n && runes[j] != ']' {
		j++
	}
	if j >= n {
		return "", 0, false
	}

	content := runes[start:j]
	frags := parseClassFragments(content)

	var b strings.Builder
	b.WriteByte('[')
	if negate {
		b.WriteByte('^')
		for _, f := range frags {
			writeClassFragment(&b, f, false)
		}
		// Always excluded, whether or not it was ever listed: a negated
		// bracket class must not match '/' either.
		b.WriteRune(slashRune)
		b.WriteByte(']')
		return b.String(), j, true
	}

	before := b.Len()
	for _, f := range frags {
		writeClassFragment(&b, f, true)
	}
	if b.Len() == before {
		// Every fragment was '/' itself, or a range that collapsed entirely
		// into it (e.g. the pattern "[/]") -- this class can, correctly,
		// never match anything.
		return neverMatchClass, j, true
	}
	b.WriteByte(']')
	return b.String(), j, true
}

// classFragment is one literal rune or inclusive range as parsed from a
// bracket expression's content, before any '/' exclusion is applied.
type classFragment struct {
	isRange bool
	r       rune // literal member (isRange == false)
	lo, hi  rune // inclusive range bounds (isRange == true), lo <= hi
}

// parseClassFragments splits a bracket expression's content into literal and
// range members. A "-" strictly between two members denotes a range in both
// glob and Go regexp syntax; it is folded into one only when the bounds are
// ascending (low <= high) and the member immediately before it is a plain
// literal equal to that low bound -- otherwise (a reversed range such as
// "z-a", or one endpoint already claimed by a neighbouring range, e.g. chained
// "a-c-f") the "-" is kept as a literal member instead, exactly as
// regexp.MustCompile would panic on a reversed range ("invalid character
// class range") if it were emitted as one.
func parseClassFragments(content []rune) []classFragment {
	var frags []classFragment
	for k := 0; k < len(content); k++ {
		r := content[k]
		if r == '-' && k > 0 && k+1 < len(content) {
			lo, hi := content[k-1], content[k+1]
			if lo <= hi && len(frags) > 0 {
				if last := frags[len(frags)-1]; !last.isRange && last.r == lo {
					frags[len(frags)-1] = classFragment{isRange: true, lo: lo, hi: hi}
					k++ // hi is consumed by the range, not its own fragment
					continue
				}
			}
			frags = append(frags, classFragment{r: '-'})
			continue
		}
		frags = append(frags, classFragment{r: r})
	}
	return frags
}

// writeClassFragment appends one fragment's regexp source to b. excludeSlash
// is set for a positive class (a negated one excludes '/' once, after every
// fragment -- see parseBracketClass): any part of the fragment that is, or
// spans, '/' is carved out rather than written, by dropping a lone '/'
// literal outright and by splitting a spanning range into the sub-ranges on
// either side of it.
func writeClassFragment(b *strings.Builder, f classFragment, excludeSlash bool) {
	if !f.isRange {
		if excludeSlash && f.r == slashRune {
			return
		}
		writeClassRune(b, f.r)
		return
	}
	lo, hi := f.lo, f.hi
	if excludeSlash && lo <= slashRune && slashRune <= hi {
		if lo <= slashRune-1 {
			writeClassRange(b, lo, slashRune-1)
		}
		if slashRune+1 <= hi {
			writeClassRange(b, slashRune+1, hi)
		}
		return
	}
	writeClassRange(b, lo, hi)
}

// writeClassRange appends an inclusive range (or, when it has collapsed to a
// single rune, a literal) as bracket-expression members.
func writeClassRange(b *strings.Builder, lo, hi rune) {
	if lo == hi {
		writeClassRune(b, lo)
		return
	}
	writeClassRune(b, lo)
	b.WriteByte('-')
	writeClassRune(b, hi)
}

// writeClassRune appends one rune as a bracket-expression member, escaping
// the runes that are still special inside a Go regexp class: "^" (only
// meaningful as the class's first rune, already spoken for by negate),
// "\\" (so it cannot be read as the start of a regexp escape the glob syntax
// never offered), "[" (left bare, a sequence like "[:foo:]" inside the class
// would be parsed by Go's regexp engine as an (invalid) POSIX character class
// and panic, even though shell globs give "[" and ":" no such meaning here),
// and "-" (unambiguous either way, but escaping it means a literal member
// never has to worry about landing in a position -- first, last, or, via the
// splitting above, freshly adjacent to another member -- where it would
// instead be read as a range operator).
func writeClassRune(b *strings.Builder, r rune) {
	switch r {
	case '^', '\\', '[', '-':
		b.WriteByte('\\')
		b.WriteRune(r)
	default:
		b.WriteRune(r)
	}
}

// Kind is the inferred repository type, spelled like hub.Kind.
const (
	KindDataset = "dataset"
	KindModel   = "model"
)

// modelExts are weight-file extensions strongly indicative of a model repo.
var modelExts = map[string]bool{
	".safetensors": true,
	".bin":         true,
	".pt":          true,
	".pth":         true,
	".ckpt":        true,
	".gguf":        true,
	".onnx":        true,
	".h5":          true,
	".msgpack":     true,
	".pb":          true,
	".tflite":      true,
}

// modelConfigNames are transformers-style config file names indicative of a
// model repo.
var modelConfigNames = map[string]bool{
	"config.json":                  true,
	"generation_config.json":       true,
	"tokenizer.json":               true,
	"tokenizer_config.json":        true,
	"adapter_config.json":          true,
	"model.safetensors.index.json": true,
}

// InferKind guesses dataset vs model from the file list: any file whose
// extension is a weight format (.safetensors .bin .pt .pth .ckpt .gguf .onnx
// .h5 .msgpack .pb .tflite) or whose base name is a transformers config
// (config.json, generation_config.json, tokenizer.json, tokenizer_config.json,
// adapter_config.json, model.safetensors.index.json) makes it a model;
// otherwise it is a dataset (.parquet .csv .jsonl .arrow .tsv .json .txt
// and anything else). reason is a short human explanation ("found
// model.safetensors" / "found data/train.parquet" / "no recognisable files").
func InferKind(files []File) (kind string, reason string) {
	for _, f := range files {
		base := path.Base(f.RepoPath)
		lower := strings.ToLower(base)
		if modelConfigNames[lower] {
			return KindModel, "found " + f.RepoPath
		}
		if modelExts[strings.ToLower(filepath.Ext(base))] {
			return KindModel, "found " + f.RepoPath
		}
	}
	if len(files) == 0 {
		return KindDataset, "no recognisable files"
	}
	return KindDataset, "found " + files[0].RepoPath
}

var (
	invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	dashRun          = regexp.MustCompile(`-{2,}`)
)

// RepoNameFromPath derives a repository name from a path: the base name of
// the cleaned absolute path (for a single file, the base name without its
// extension), with characters outside [A-Za-z0-9._-] replaced by "-", runs of
// "-" collapsed, leading/trailing "-" and "." trimmed, truncated to 96
// characters, and a trailing ".git" (plus any "-"/"." this newly exposes)
// stripped repeatedly. An empty result (e.g. "/") is an error. The server's
// rule is: 1-96 chars of letters, digits, dot, dash or underscore, starting
// with a letter or digit, not ending in ".git".
func RepoNameFromPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("local: %w", err)
	}
	clean := filepath.Clean(abs)
	base := filepath.Base(clean)

	// A path we can't stat (e.g. it doesn't exist yet) is treated as a
	// directory: only a confirmed regular file gets its extension stripped.
	if info, statErr := os.Stat(clean); statErr == nil && !info.IsDir() {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}

	name := invalidNameChars.ReplaceAllString(base, "-")
	name = dashRun.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if len(name) > 96 {
		name = name[:96]
	}
	// Truncating to 96 bytes can land the cut exactly on a literal ".git"
	// that was never a suffix of the original name (just four characters
	// that happened to end up last), or expose a "-"/"." that an earlier trim
	// already removed once. Stripping only before truncation -- as this used
	// to do -- misses that case entirely, so the server's "not ending in
	// .git" rule still rejected the derived name. Repeat until a pass changes
	// nothing: removing ".git" can reveal a "-." to trim and vice versa.
	for {
		trimmed := strings.TrimSuffix(name, ".git")
		trimmed = strings.Trim(trimmed, "-.")
		if trimmed == name {
			break
		}
		name = trimmed
	}

	if name == "" || !isAlnumByte(name[0]) {
		return "", fmt.Errorf("local: cannot derive a repository name from %q", p)
	}
	return name, nil
}

func isAlnumByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// ParseTarget parses the --to flag: "ns/name" or just "name" (ns == "").
// Leading "datasets/" / "models/" prefixes are stripped and reported via kind
// ("" when absent). Anything with more than one "/" (after the prefix) or an
// empty component is an error.
func ParseTarget(s string) (kind, ns, name string, err error) {
	rest := s
	switch {
	case strings.HasPrefix(rest, "datasets/"):
		kind, rest = KindDataset, strings.TrimPrefix(rest, "datasets/")
	case strings.HasPrefix(rest, "dataset/"):
		kind, rest = KindDataset, strings.TrimPrefix(rest, "dataset/")
	case strings.HasPrefix(rest, "models/"):
		kind, rest = KindModel, strings.TrimPrefix(rest, "models/")
	case strings.HasPrefix(rest, "model/"):
		kind, rest = KindModel, strings.TrimPrefix(rest, "model/")
	}

	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		name = parts[0]
	case 2:
		ns, name = parts[0], parts[1]
	default:
		return "", "", "", fmt.Errorf("local: invalid target %q", s)
	}
	if name == "" || (len(parts) == 2 && ns == "") {
		return "", "", "", fmt.Errorf("local: invalid target %q", s)
	}
	return kind, ns, name, nil
}
