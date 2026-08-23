package gitrepo

import (
	"path"
	"strings"
)

// DefaultGitAttributes is written into every new repository so that large
// binary formats go to LFS from the first push, matching what a HF repository
// ships with.
const DefaultGitAttributes = `*.7z filter=lfs diff=lfs merge=lfs -text
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
// name at any depth, while a pattern containing a slash is anchored.
func matchAttrPattern(pattern, filePath string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	if strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, filePath)
		return err == nil && ok
	}
	ok, err := path.Match(pattern, path.Base(filePath))
	return err == nil && ok
}
