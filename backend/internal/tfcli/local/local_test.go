package local

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDirectory(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, "data", "train.parquet"), "parquet-bytes")
	writeFile(t, filepath.Join(root, "data", "nested", "b.csv"), "a,b\n")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main")
	writeFile(t, filepath.Join(root, "__pycache__", "mod.pyc"), "junk")
	writeFile(t, filepath.Join(root, ".DS_Store"), "junk")
	writeFile(t, filepath.Join(root, "Thumbs.db"), "junk")

	if runtime.GOOS != "windows" {
		linkTarget := filepath.Join(root, "data")
		if err := os.Symlink(linkTarget, filepath.Join(root, "data-link")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		fileLinkTarget := filepath.Join(root, "README.md")
		if err := os.Symlink(fileLinkTarget, filepath.Join(root, "README-link.md")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}

	files, _, _, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var got []string
	for _, f := range files {
		got = append(got, f.RepoPath)
		if f.AbsPath == "" {
			t.Errorf("file %s has empty AbsPath", f.RepoPath)
		}
	}
	sort.Strings(got)

	want := []string{"README.md", "data/nested/b.csv", "data/train.parquet"}
	if runtime.GOOS != "windows" {
		want = append(want, "README-link.md")
		sort.Strings(want)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan RepoPaths = %v, want %v", got, want)
	}

	// Results must be sorted by RepoPath.
	for i := 1; i < len(files); i++ {
		if files[i-1].RepoPath > files[i].RepoPath {
			t.Fatalf("results not sorted: %s > %s", files[i-1].RepoPath, files[i].RepoPath)
		}
	}
}

func TestScanRootIsSymlinkToDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}

	realDir := t.TempDir()
	writeFile(t, filepath.Join(realDir, "README.md"), "hello")
	writeFile(t, filepath.Join(realDir, "data", "train.parquet"), "parquet-bytes")

	linkParent := t.TempDir()
	root := filepath.Join(linkParent, "root-link")
	if err := os.Symlink(realDir, root); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	files, allPaths, _, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var got []string
	for _, f := range files {
		got = append(got, f.RepoPath)
		if f.AbsPath == "" {
			t.Errorf("file %s has empty AbsPath", f.RepoPath)
		}
	}
	sort.Strings(got)

	want := []string{"README.md", "data/train.parquet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan RepoPaths = %v, want %v", got, want)
	}

	sort.Strings(allPaths)
	if !reflect.DeepEqual(allPaths, want) {
		t.Fatalf("Scan allPaths = %v, want %v", allPaths, want)
	}
}

func TestScanSingleFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "model.safetensors")
	writeFile(t, p, "weights")

	files, _, _, err := Scan(p, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("Scan() = %d files, want 1", len(files))
	}
	if files[0].RepoPath != "model.safetensors" {
		t.Errorf("RepoPath = %q, want %q", files[0].RepoPath, "model.safetensors")
	}
}

func TestScanMissingRoot(t *testing.T) {
	if _, _, _, err := Scan(filepath.Join(t.TempDir(), "nope"), Options{}); err == nil {
		t.Fatal("Scan() on missing root: want error, got nil")
	}
}

func TestScanIncludeExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data", "train.parquet"), "x")
	writeFile(t, filepath.Join(root, "data", "test.parquet"), "x")
	writeFile(t, filepath.Join(root, "notes.txt"), "x")

	files, _, _, err := Scan(root, Options{
		Include: []string{"**/*.parquet"},
		Exclude: []string{"**/test.parquet"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(files) != 1 || files[0].RepoPath != "data/train.parquet" {
		t.Fatalf("Scan() = %+v, want just data/train.parquet", files)
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern  string
		repoPath string
		want     bool
	}{
		{"*.parquet", "a.parquet", true},
		{"*.parquet", "data/train.parquet", true}, // base-name fallback
		{"**/*.parquet", "a.parquet", true},
		{"**/*.parquet", "x/y/a.parquet", true},
		{"**/*.parquet", "x/y/a.csv", false},
		{"data/**", "data/a.parquet", true},
		{"data/**", "data/nested/b.csv", true},
		{"data/**", "other/a.parquet", false},
		{"data", "data/a.parquet", true},
		{"data/", "data/nested/b.csv", true},
		{"data", "database/a.parquet", false},
		{"?", "a", true},
		{"?", "ab", false},
		{"a/b.txt", "x/a/b.txt", false}, // pattern with "/" must not match basename-only
		{"a/b.txt", "a/b.txt", true},
		{"", "a.txt", false},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.repoPath); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.repoPath, got, tt.want)
		}
	}
}

func TestInferKind(t *testing.T) {
	tests := []struct {
		name       string
		files      []File
		wantKind   string
		wantReason string
	}{
		{
			name:       "safetensors is a model",
			files:      []File{{RepoPath: "model.safetensors"}},
			wantKind:   KindModel,
			wantReason: "found model.safetensors",
		},
		{
			name:       "config.json is a model",
			files:      []File{{RepoPath: "config.json"}},
			wantKind:   KindModel,
			wantReason: "found config.json",
		},
		{
			name:       "parquet only is a dataset",
			files:      []File{{RepoPath: "data/train.parquet"}},
			wantKind:   KindDataset,
			wantReason: "found data/train.parquet",
		},
		{
			name:       "empty is a dataset",
			files:      nil,
			wantKind:   KindDataset,
			wantReason: "no recognisable files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, reason := InferKind(tt.files)
			if kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", kind, tt.wantKind)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestRepoNameFromPath(t *testing.T) {
	dir := t.TempDir()

	mustMkdir := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mustFile := func(rel string) string {
		p := filepath.Join(dir, rel)
		writeFile(t, p, "x")
		return p
	}

	dataSetDir := mustMkdir("My Data Set")
	gitDir := mustMkdir("foo.git")
	parquetFile := mustFile("x.parquet")

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "directory with spaces", path: dataSetDir + string(filepath.Separator), want: "My-Data-Set"},
		{name: "single file strips extension", path: parquetFile, want: "x"},
		{name: "git clone dir strips .git suffix", path: gitDir, want: "foo"},
		{name: "root is an error", path: "/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoNameFromPath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RepoNameFromPath(%q) = %q, want error", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RepoNameFromPath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("RepoNameFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in      string
		kind    string
		ns      string
		name    string
		wantErr bool
	}{
		{in: "name", name: "name"},
		{in: "ns/name", ns: "ns", name: "name"},
		{in: "datasets/name", kind: KindDataset, name: "name"},
		{in: "datasets/ns/name", kind: KindDataset, ns: "ns", name: "name"},
		{in: "dataset/name", kind: KindDataset, name: "name"},
		{in: "models/name", kind: KindModel, name: "name"},
		{in: "model/ns/name", kind: KindModel, ns: "ns", name: "name"},
		{in: "", wantErr: true},
		{in: "ns/name/extra", wantErr: true},
		{in: "/name", wantErr: true},
		{in: "ns/", wantErr: true},
		{in: "datasets/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			kind, ns, name, err := ParseTarget(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTarget(%q) = (%q,%q,%q), want error", tt.in, kind, ns, name)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTarget(%q): %v", tt.in, err)
			}
			if kind != tt.kind || ns != tt.ns || name != tt.name {
				t.Errorf("ParseTarget(%q) = (%q,%q,%q), want (%q,%q,%q)", tt.in, kind, ns, name, tt.kind, tt.ns, tt.name)
			}
		})
	}
}

// TestScanReportsSkippedPathsAndBlindSpots pins the contract `tf up --delete`
// depends on: a path Scan leaves out of files is still reported as present on
// disk, and a directory Scan never looked inside is reported as a blind spot
// rather than as an empty one. Getting this wrong deletes remote files that
// are sitting right there locally.
func TestScanReportsSkippedPathsAndBlindSpots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}

	realData := t.TempDir()
	writeFile(t, filepath.Join(realData, "train.parquet"), "parquet-bytes")
	writeFile(t, filepath.Join(realData, "nested", "b.csv"), "a,b\n")

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, ".DS_Store"), "junk")
	writeFile(t, filepath.Join(root, "__pycache__", "mod.pyc"), "junk")
	if err := os.Symlink(realData, filepath.Join(root, "data")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "gone.txt"), filepath.Join(root, "dangling.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	files, allPaths, skipped, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var got []string
	for _, f := range files {
		got = append(got, f.RepoPath)
	}
	if !reflect.DeepEqual(got, []string{"README.md"}) {
		t.Fatalf("Scan files = %v, want just README.md", got)
	}

	// Everything the walk found on disk is reported, uploaded or not.
	wantAll := []string{".DS_Store", "README.md", "dangling.txt", "data"}
	if !reflect.DeepEqual(allPaths, wantAll) {
		t.Fatalf("Scan allPaths = %v, want %v", allPaths, wantAll)
	}

	byPath := map[string]Skipped{}
	for _, s := range skipped {
		byPath[s.RepoPath] = s
	}
	dataSkip, ok := byPath["data"]
	if !ok {
		t.Fatalf("skipped = %+v, want an entry for the symlinked directory", skipped)
	}
	if !dataSkip.Dir || dataSkip.Reason != ReasonSymlinkDir || !dataSkip.Notable() {
		t.Errorf("skipped data = %+v, want a notable directory skip with reason %q", dataSkip, ReasonSymlinkDir)
	}
	if s, ok := byPath["dangling.txt"]; !ok || s.Dir || s.Reason != ReasonBrokenSymlink {
		t.Errorf("skipped dangling.txt = %+v (present=%v), want a non-directory broken-symlink skip", s, ok)
	}
	pycache, ok := byPath["__pycache__"]
	if !ok || !pycache.Dir {
		t.Fatalf("skipped = %+v, want __pycache__ recorded as an unread directory", skipped)
	}
	if pycache.Notable() {
		t.Error("__pycache__ is a documented, routine exclusion and must not be warned about")
	}
	// .DS_Store is in allPaths (it exists), so it needs no blind-spot entry.
	if _, ok := byPath[".DS_Store"]; ok {
		t.Error(".DS_Store is reported in allPaths and must not also be a skip entry")
	}

	dirs := SkippedDirs(skipped)
	sort.Strings(dirs)
	if !reflect.DeepEqual(dirs, []string{"__pycache__", "data"}) {
		t.Fatalf("SkippedDirs = %v, want [__pycache__ data]", dirs)
	}
}

// TestScanRecordsIgnoredGitDirectory keeps ".git" out of the user-facing
// warnings while still recording it as unread.
func TestScanRecordsIgnoredGitDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main")

	_, allPaths, skipped, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !reflect.DeepEqual(allPaths, []string{"README.md"}) {
		t.Fatalf("Scan allPaths = %v, want just README.md", allPaths)
	}
	if len(skipped) != 1 || skipped[0].RepoPath != ".git" || !skipped[0].Dir || skipped[0].Notable() {
		t.Fatalf("skipped = %+v, want one quiet, unread .git directory", skipped)
	}
}

// TestScanExcludedFileStaysOnDisk is the older half of the same rule: a file
// --exclude kept out of the upload is still on disk.
func TestScanExcludedFileStaysOnDisk(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), "x")
	writeFile(t, filepath.Join(root, "skip.txt"), "x")

	files, allPaths, _, err := Scan(root, Options{Exclude: []string{"skip.txt"}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(files) != 1 || files[0].RepoPath != "keep.txt" {
		t.Fatalf("Scan files = %+v, want just keep.txt", files)
	}
	if !reflect.DeepEqual(allPaths, []string{"keep.txt", "skip.txt"}) {
		t.Fatalf("Scan allPaths = %v, want both files", allPaths)
	}
}

// TestScanSkipsHiddenPathsByDefault covers the credential-leak rule: a
// project directory routinely holds ".env" / ".envrc" / ".aws/credentials"
// next to the data, and repositories here are world-readable.
func TestScanSkipsHiddenPathsByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, ".env"), "TOKEN=secret")
	writeFile(t, filepath.Join(root, ".envrc"), "export TOKEN=secret")
	writeFile(t, filepath.Join(root, "conf", ".secret.json"), "{}")
	writeFile(t, filepath.Join(root, ".aws", "credentials"), "[default]")
	// Repository content rather than machine state: these keep travelling.
	writeFile(t, filepath.Join(root, ".gitattributes"), "*.parquet filter=lfs")
	writeFile(t, filepath.Join(root, ".gitignore"), "*.pyc")

	files, allPaths, skipped, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var got []string
	for _, f := range files {
		got = append(got, f.RepoPath)
	}
	want := []string{".gitattributes", ".gitignore", "README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan files = %v, want %v", got, want)
	}

	// The skipped dot-files are still on disk, which is what keeps
	// `tf up --delete` from reading their absence as "deleted locally".
	// The hidden *directory* is not listed (allPaths only ever holds files);
	// SkippedDirs below is what keeps --delete off everything under it.
	wantAll := []string{".env", ".envrc", ".gitattributes", ".gitignore", "README.md", "conf/.secret.json"}
	if !reflect.DeepEqual(allPaths, wantAll) {
		t.Fatalf("Scan allPaths = %v, want %v", allPaths, wantAll)
	}

	byPath := map[string]Skipped{}
	for _, s := range skipped {
		byPath[s.RepoPath] = s
	}
	for _, p := range []string{".env", ".envrc", "conf/.secret.json"} {
		s, ok := byPath[p]
		if !ok {
			t.Fatalf("skipped = %+v, want an entry for %s", skipped, p)
		}
		if s.Dir || s.Reason != ReasonHidden || !s.Notable() {
			t.Errorf("skipped %s = %+v, want a notable non-directory %q skip", p, s, ReasonHidden)
		}
	}
	aws, ok := byPath[".aws"]
	if !ok || !aws.Dir || aws.Reason != ReasonHidden || !aws.Notable() {
		t.Fatalf("skipped .aws = %+v (present=%v), want a notable unread hidden directory", aws, ok)
	}
	if !reflect.DeepEqual(SkippedDirs(skipped), []string{".aws"}) {
		t.Fatalf("SkippedDirs = %v, want [.aws]", SkippedDirs(skipped))
	}
}

// TestScanHiddenOptionUploadsThem is the opt-back-in half of the rule.
func TestScanHiddenOptionUploadsThem(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	writeFile(t, filepath.Join(root, ".env"), "TOKEN=secret")
	writeFile(t, filepath.Join(root, ".aws", "credentials"), "[default]")

	files, _, skipped, err := Scan(root, Options{Hidden: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.RepoPath)
	}
	want := []string{".aws/credentials", ".env", "README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan files = %v, want %v", got, want)
	}
	for _, s := range skipped {
		if s.Reason == ReasonHidden {
			t.Errorf("--hidden must produce no hidden skips, got %+v", s)
		}
	}
}

// TestScanHiddenRootIsStillScanned: the rule is about what the walk finds
// *inside* the tree. A path the user typed is a path the user chose.
func TestScanHiddenRootIsStillScanned(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".hidden-project")
	writeFile(t, filepath.Join(root, "train.parquet"), "x")

	files, _, _, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(files) != 1 || files[0].RepoPath != "train.parquet" {
		t.Fatalf("Scan files = %+v, want just train.parquet", files)
	}

	// Same for a single hidden file named directly.
	single := filepath.Join(parent, ".env")
	writeFile(t, single, "TOKEN=secret")
	files, _, _, err = Scan(single, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(files) != 1 || files[0].RepoPath != ".env" {
		t.Fatalf("Scan files = %+v, want the file the user named", files)
	}
}

// TestScanHiddenSymlinkedDirectoryStaysABlindSpot: a dot-named symlink to a
// directory must still be reported as an unread directory, or --delete would
// treat everything the remote holds under it as gone locally.
func TestScanHiddenSymlinkedDirectoryStaysABlindSpot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	realData := t.TempDir()
	writeFile(t, filepath.Join(realData, "train.parquet"), "x")

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello")
	if err := os.Symlink(realData, filepath.Join(root, ".cache")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, _, skipped, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !reflect.DeepEqual(SkippedDirs(skipped), []string{".cache"}) {
		t.Fatalf("SkippedDirs = %v, want [.cache]", SkippedDirs(skipped))
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
