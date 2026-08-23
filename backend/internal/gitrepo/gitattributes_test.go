package gitrepo

import "testing"

func TestShouldUseLFS_PatternMatch(t *testing.T) {
	rules := ParseGitAttributes([]byte(DefaultGitAttributes("model")))

	if !rules.ShouldUseLFS("data.parquet", 10) {
		t.Errorf("*.parquet at any depth should route to LFS regardless of size")
	}
	if !rules.ShouldUseLFS("nested/dir/data.parquet", 10) {
		t.Errorf("*.parquet nested should still match (bare pattern matches basename at any depth)")
	}
	if rules.ShouldUseLFS("data.txt", 10) {
		t.Errorf("plain small text file should not go to LFS")
	}
}

func TestShouldUseLFS_ExplicitOverrideWins(t *testing.T) {
	content := "*.parquet filter=lfs diff=lfs merge=lfs -text\n" +
		"special.parquet -filter=lfs\n"
	rules := ParseGitAttributes([]byte(content))

	if rules.ShouldUseLFS("special.parquet", 1) {
		t.Errorf("special.parquet has an explicit -filter=lfs override, should NOT use LFS")
	}
	if !rules.ShouldUseLFS("other.parquet", 1) {
		t.Errorf("other.parquet should still match *.parquet and use LFS")
	}
}

func TestShouldUseLFS_LaterRuleWinsOnSamePattern(t *testing.T) {
	// Two rules for the exact same pattern: the later one must take
	// precedence, matching git's own "last match wins" semantics.
	content := "*.bin filter=lfs\n*.bin -filter=lfs\n"
	rules := ParseGitAttributes([]byte(content))
	if rules.ShouldUseLFS("model.bin", 1) {
		t.Errorf("later rule (-filter=lfs) should win over the earlier filter=lfs rule")
	}

	content2 := "*.bin -filter=lfs\n*.bin filter=lfs\n"
	rules2 := ParseGitAttributes([]byte(content2))
	if !rules2.ShouldUseLFS("model.bin", 1) {
		t.Errorf("later rule (filter=lfs) should win over the earlier -filter=lfs rule")
	}
}

func TestShouldUseLFS_NoPatternFallsBackToSizeThreshold(t *testing.T) {
	rules := ParseGitAttributes([]byte("")) // no patterns at all

	if rules.ShouldUseLFS("anything.dat", LFSInlineThreshold-1) {
		t.Errorf("size just under threshold should not use LFS")
	}
	if !rules.ShouldUseLFS("anything.dat", LFSInlineThreshold) {
		t.Errorf("size exactly at threshold should use LFS")
	}
	if !rules.ShouldUseLFS("anything.dat", LFSInlineThreshold+1) {
		t.Errorf("size above threshold should use LFS")
	}
}

func TestShouldUseLFS_NilRulesFallsBackToSizeThreshold(t *testing.T) {
	var rules *LFSRules // nil receiver must not panic
	if rules.ShouldUseLFS("x.bin", LFSInlineThreshold-1) {
		t.Errorf("nil rules: size under threshold should not use LFS")
	}
	if !rules.ShouldUseLFS("x.bin", LFSInlineThreshold) {
		t.Errorf("nil rules: size at threshold should use LFS")
	}
}

func TestShouldUseLFS_SlashPatternIsAnchored(t *testing.T) {
	content := "data/*.bin filter=lfs\n"
	rules := ParseGitAttributes([]byte(content))

	if !rules.ShouldUseLFS("data/file.bin", 1) {
		t.Errorf("data/*.bin should match data/file.bin")
	}
	if rules.ShouldUseLFS("other/data/file.bin", 1) {
		t.Errorf("data/*.bin is anchored at the repo root; other/data/file.bin should NOT match, " +
			"and with no other rule matching it should fall back to the size threshold (got true for a 1-byte file)")
	}
	if rules.ShouldUseLFS("file.bin", 1) {
		t.Errorf("data/*.bin should not match a root-level file.bin outside the data/ directory")
	}
}

func TestParseGitAttributes_IgnoresCommentsAndBlankLines(t *testing.T) {
	content := "# a comment\n\n*.parquet filter=lfs diff=lfs merge=lfs -text\n\n# trailing comment\n"
	rules := ParseGitAttributes([]byte(content))
	if !rules.ShouldUseLFS("x.parquet", 1) {
		t.Errorf("*.parquet rule should still be parsed despite comments/blank lines around it")
	}
}

// The seeded list and the fallback both promise LFS routing for a dataset's
// media payload; a model repository must not pay an LFS round trip for the
// screenshots in its model card.
func TestDefaultGitAttributes_KindSpecific(t *testing.T) {
	model := ParseGitAttributes([]byte(DefaultGitAttributes("model")))
	dataset := ParseGitAttributes([]byte(DefaultGitAttributes("dataset")))

	if model.ShouldUseLFS("assets/screenshot.png", 4096) {
		t.Errorf("small png in a model repository should stay an ordinary blob")
	}
	if !dataset.ShouldUseLFS("audio/train/0001.wav", 4096) {
		t.Errorf("wav in a dataset repository should route to LFS regardless of size")
	}
	if !dataset.ShouldUseLFS("images/0001.png", 4096) {
		t.Errorf("png in a dataset repository should route to LFS regardless of size")
	}
	// Everything the model list covers stays covered for datasets too.
	for _, p := range []string{"model.safetensors", "data/train.parquet", "weights.gguf"} {
		if !model.ShouldUseLFS(p, 10) || !dataset.ShouldUseLFS(p, 10) {
			t.Errorf("%s should route to LFS for both kinds", p)
		}
	}
	// An unknown kind must not silently gain the dataset rules.
	if ParseGitAttributes([]byte(DefaultGitAttributes(""))).ShouldUseLFS("images/0001.png", 4096) {
		t.Errorf("unknown kind should fall back to the model list")
	}
}

// "**" is what git-lfs matches on the client, so the server has to match it
// too: the same file must not take one route on `git push` and another through
// the upload API.
func TestShouldUseLFS_DoubleStar(t *testing.T) {
	rules := ParseGitAttributes([]byte("data/**/*.bin filter=lfs diff=lfs merge=lfs -text\n"))

	for _, p := range []string{"data/x.bin", "data/a/x.bin", "data/a/b/c/x.bin"} {
		if !rules.ShouldUseLFS(p, 10) {
			t.Errorf("%s should match data/**/*.bin at any depth", p)
		}
	}
	for _, p := range []string{"x.bin", "other/a/x.bin", "data/a/x.txt"} {
		if rules.ShouldUseLFS(p, 10) {
			t.Errorf("%s should not match data/**/*.bin", p)
		}
	}

	// The shape the seeded list uses.
	seeded := ParseGitAttributes([]byte(DefaultGitAttributes("model")))
	for _, p := range []string{"saved_model/saved_model.pb", "saved_model/1/variables/variables.index"} {
		if !seeded.ShouldUseLFS(p, 10) {
			t.Errorf("%s should match saved_model/**/*", p)
		}
	}
	if seeded.ShouldUseLFS("other/saved_model.txt", 10) {
		t.Errorf("saved_model/**/* is anchored to the repository root")
	}

	// A leading "**/" matches at the root as well as below it; a trailing
	// "/**" matches everything inside.
	anywhere := ParseGitAttributes([]byte("**/*.npy filter=lfs diff=lfs merge=lfs -text\n"))
	for _, p := range []string{"x.npy", "a/x.npy", "a/b/x.npy"} {
		if !anywhere.ShouldUseLFS(p, 10) {
			t.Errorf("%s should match **/*.npy", p)
		}
	}
	inside := ParseGitAttributes([]byte("blobs/** filter=lfs diff=lfs merge=lfs -text\n"))
	if !inside.ShouldUseLFS("blobs/a/b/c", 10) {
		t.Errorf("blobs/** should match everything inside blobs/")
	}
	if inside.ShouldUseLFS("blobs", 10) {
		t.Errorf("blobs/** should not match the directory entry itself")
	}
}

// An anchored pattern with no "**" keeps its old, path.Match-shaped meaning:
// one glob per segment, never crossing a separator.
func TestShouldUseLFS_AnchoredPatternDoesNotCrossSeparators(t *testing.T) {
	rules := ParseGitAttributes([]byte("data/*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	if !rules.ShouldUseLFS("data/x.bin", 10) {
		t.Errorf("data/x.bin should match data/*.bin")
	}
	if rules.ShouldUseLFS("data/a/x.bin", 10) {
		t.Errorf("data/*.bin must not match across a separator")
	}
}
