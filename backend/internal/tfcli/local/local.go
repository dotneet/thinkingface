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

// Scan walks root. When root is a directory every regular file below it is
// returned with RepoPath relative to root; when root is a single file, one
// entry with RepoPath = its base name. Always skipped: ".git" directories,
// ".DS_Store", "Thumbs.db", "__pycache__" directories, and symlinks that point
// at directories (to avoid loops; symlinked files are followed). Results are
// sorted by RepoPath. A root that does not exist is an error.
//
// allPaths reports every RepoPath the walk visited (subject only to the
// always-skipped names above, never to Include/Exclude): callers that need to
// know what actually exists on disk -- as opposed to what --include/--exclude
// selected for upload -- use this instead of re-scanning. `tf up --delete`
// is the motivating case: a file excluded from the upload but still on disk
// must not be treated as "gone locally" and deleted from the remote. A single
// walk produces both slices so scanning a large tree twice is never needed.
func Scan(root string, opts Options) (files []File, allPaths []string, err error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, fmt.Errorf("local: scan %s: %w", root, err)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("local: scan %s: %w", root, err)
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
		return files, allPaths, nil
	}

	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == rootAbs {
			return nil
		}
		name := d.Name()

		if d.IsDir() {
			if name == ".git" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			target, statErr := os.Stat(p)
			if statErr != nil {
				// Broken symlink: nothing to upload.
				return nil
			}
			if target.IsDir() {
				// Do not follow symlinked directories (avoids cycles).
				return nil
			}
			if name == ".DS_Store" || name == "Thumbs.db" {
				return nil
			}
			rel, relErr := filepath.Rel(rootAbs, p)
			if relErr != nil {
				return relErr
			}
			f := File{RepoPath: filepath.ToSlash(rel), AbsPath: p, Size: target.Size()}
			allPaths = append(allPaths, f.RepoPath)
			if matchFilters(f.RepoPath, opts) {
				files = append(files, f)
			}
			return nil
		}

		if name == ".DS_Store" || name == "Thumbs.db" {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		entryInfo, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		rel, relErr := filepath.Rel(rootAbs, p)
		if relErr != nil {
			return relErr
		}
		f := File{RepoPath: filepath.ToSlash(rel), AbsPath: p, Size: entryInfo.Size()}
		allPaths = append(allPaths, f.RepoPath)
		if matchFilters(f.RepoPath, opts) {
			files = append(files, f)
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}

	sort.Slice(files, func(i, j int) bool { return files[i].RepoPath < files[j].RepoPath })
	sort.Strings(allPaths)
	return files, allPaths, nil
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

// Match reports whether pattern matches repoPath. Patterns are shell globs
// with "**" matching any number of path segments (so "**/*.parquet" and
// "data/**" work); a pattern with no "/" is also tried against the base name
// ("*.parquet" matches "data/train.parquet"). A pattern that names a
// directory ("data" or "data/") matches everything beneath it.
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
	re := regexp.MustCompile(globToRegexpSource(pattern))
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

func segmentToRegexpSource(seg string) string {
	var b strings.Builder
	for _, r := range seg {
		switch r {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
// "-" collapsed, leading/trailing "-" and "." trimmed, a ".git" suffix
// removed, and truncated to 96 characters. An empty result (e.g. "/") is an
// error. The server's rule is: 1-96 chars of letters, digits, dot, dash or
// underscore, starting with a letter or digit, not ending in ".git".
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
	name = strings.TrimSuffix(name, ".git")
	name = strings.Trim(name, "-.")
	if len(name) > 96 {
		name = strings.TrimRight(name[:96], "-.")
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
