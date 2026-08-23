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

	files, err := Scan(root, Options{})
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

func TestScanSingleFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "model.safetensors")
	writeFile(t, p, "weights")

	files, err := Scan(p, Options{})
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
	if _, err := Scan(filepath.Join(t.TempDir(), "nope"), Options{}); err == nil {
		t.Fatal("Scan() on missing root: want error, got nil")
	}
}

func TestScanIncludeExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data", "train.parquet"), "x")
	writeFile(t, filepath.Join(root, "data", "test.parquet"), "x")
	writeFile(t, filepath.Join(root, "notes.txt"), "x")

	files, err := Scan(root, Options{
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
