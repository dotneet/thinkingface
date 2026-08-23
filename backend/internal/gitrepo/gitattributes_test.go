package gitrepo

import "testing"

func TestShouldUseLFS_PatternMatch(t *testing.T) {
	rules := ParseGitAttributes([]byte(DefaultGitAttributes))

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
