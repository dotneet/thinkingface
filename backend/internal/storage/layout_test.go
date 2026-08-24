package storage

import (
	"strings"
	"testing"
)

func TestLFSKey_ShapesAsTwoLevelFanOut(t *testing.T) {
	oid := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	got := LFSKey(oid)
	want := "lfs/ab/cd/" + oid
	if got != want {
		t.Errorf("LFSKey(%q) = %q, want %q", oid, got, want)
	}
}

func TestLFSKey_ShortInputDoesNotPanic(t *testing.T) {
	tests := []struct {
		oid  string
		want string
	}{
		{"", "lfs/"},
		{"a", "lfs/a"},
		{"ab", "lfs/ab"},
		{"abc", "lfs/abc"},
	}
	for _, tt := range tests {
		t.Run(tt.oid, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("LFSKey(%q) panicked: %v", tt.oid, r)
				}
			}()
			got := LFSKey(tt.oid)
			if got != tt.want {
				t.Errorf("LFSKey(%q) = %q, want %q", tt.oid, got, tt.want)
			}
		})
	}
}

func TestLFSKey_ExactlyFourCharsUsesFanOut(t *testing.T) {
	got := LFSKey("abcd")
	want := "lfs/ab/cd/abcd"
	if got != want {
		t.Errorf("LFSKey(4-char oid) = %q, want %q", got, want)
	}
}

func TestBlobKey_ShapesAsTwoLevelFanOut(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	got := BlobKey(sha)
	want := "blobs/01/23/" + sha
	if got != want {
		t.Errorf("BlobKey(%q) = %q, want %q", sha, got, want)
	}
}

func TestBlobKey_ShortInputDoesNotPanic(t *testing.T) {
	tests := []struct {
		sha  string
		want string
	}{
		{"", "blobs/"},
		{"a", "blobs/a"},
		{"ab", "blobs/ab"},
		{"abc", "blobs/abc"},
		{"abcd", "blobs/ab/cd/abcd"},
	}
	for _, tt := range tests {
		t.Run(tt.sha, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("BlobKey(%q) panicked: %v", tt.sha, r)
				}
			}()
			if got := BlobKey(tt.sha); got != tt.want {
				t.Errorf("BlobKey(%q) = %q, want %q", tt.sha, got, tt.want)
			}
		})
	}
}

// The two content-addressed layers must never collide: a sha that happens to
// look like an oid still lands under its own prefix.
func TestBlobKeyAndLFSKey_LiveInSeparatePrefixes(t *testing.T) {
	digest := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if BlobKey(digest) == LFSKey(digest) {
		t.Fatalf("BlobKey and LFSKey collide for %q", digest)
	}
	if got := BlobKey(digest)[:6]; got != "blobs/" {
		t.Errorf("BlobKey prefix = %q, want %q", got, "blobs/")
	}
	if got := LFSKey(digest)[:4]; got != "lfs/" {
		t.Errorf("LFSKey prefix = %q, want %q", got, "lfs/")
	}
}

func TestWALPrefix_KindDirShape(t *testing.T) {
	if got, want := WALPrefix(LegacyStoragePath("model", "ns", "n")), "wal/models/ns/n/"; got != want {
		t.Errorf("WALPrefix(model) = %q, want %q", got, want)
	}
	if got, want := WALPrefix(LegacyStoragePath("dataset", "ns", "n")), "wal/datasets/ns/n/"; got != want {
		t.Errorf("WALPrefix(dataset) = %q, want %q", got, want)
	}
}

func TestWALPrefix_NewStoragePathShape(t *testing.T) {
	if got, want := WALPrefix("repos/01JAV0EXAMPLE"), "wal/repos/01JAV0EXAMPLE/"; got != want {
		t.Errorf("WALPrefix(repos/…) = %q, want %q", got, want)
	}
}

func TestWALIndexKey_IsOnePerRepository(t *testing.T) {
	storagePath := LegacyStoragePath("dataset", "acme", "widgets")
	got := WALIndexKey(storagePath)
	want := "wal/datasets/acme/widgets/index.json"
	if got != want {
		t.Errorf("WALIndexKey = %q, want %q", got, want)
	}
	if prefix := WALPrefix(storagePath); got[:len(prefix)] != prefix {
		t.Errorf("WALIndexKey %q does not start with WALPrefix %q", got, prefix)
	}
}

func TestWALSubPrefixes_HaveTrailingSlash(t *testing.T) {
	storagePath := LegacyStoragePath("model", "ns", "n")
	if got, want := WALBasePrefix(storagePath), "wal/models/ns/n/base/"; got != want {
		t.Errorf("WALBasePrefix = %q, want %q", got, want)
	}
	if got, want := WALEntriesPrefix(storagePath), "wal/models/ns/n/entries/"; got != want {
		t.Errorf("WALEntriesPrefix = %q, want %q", got, want)
	}
}

func TestWALKey_ResolvesIndexRelativeNames(t *testing.T) {
	storagePath := LegacyStoragePath("model", "ns", "n")
	tests := []struct{ rel, want string }{
		{"entries/000042-01JAV.pack", "wal/models/ns/n/entries/000042-01JAV.pack"},
		{"base/01JAV.pack", "wal/models/ns/n/base/01JAV.pack"},
		{"/base/01JAV.pack", "wal/models/ns/n/base/01JAV.pack"},
	}
	for _, tt := range tests {
		if got := WALKey(storagePath, tt.rel); got != tt.want {
			t.Errorf("WALKey(%q) = %q, want %q", tt.rel, got, tt.want)
		}
	}
}

// Both staging keys have to sit under the prefix `thinkingface gc` sweeps, or
// an interrupted upload is a multi-gigabyte object nothing ever reclaims --
// nothing else records that one exists.
func TestStagingKeys_LiveUnderTheSweptPrefix(t *testing.T) {
	oid := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	for _, key := range []string{LFSStagingKey(7, oid), LFSIncomingKey(7, "deadbeef")} {
		if !strings.HasPrefix(key, LFSStagingPrefix) {
			t.Errorf("key %q is not under %q", key, LFSStagingPrefix)
		}
		if !strings.Contains(key, "/7/") {
			t.Errorf("key %q does not carry the repository id", key)
		}
	}
}

// An incoming upload is named before its digest is known, so its name must not
// be mistakable for an oid-named staging key.
func TestLFSIncomingKey_DoesNotCollideWithOIDNamedStaging(t *testing.T) {
	oid := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	if got := LFSIncomingKey(7, oid); got == LFSStagingKey(7, oid) {
		t.Errorf("LFSIncomingKey(7, oid) = %q collides with the staging key for the same oid", got)
	}
}
