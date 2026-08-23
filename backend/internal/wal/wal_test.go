package wal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

const (
	kind = "dataset"
	ns   = "acme"
	repo = "widgets"

	// storagePath is the repository's immutable physical location that these
	// tests exercise — the legacy form a pre-storage_path repository was
	// backfilled with ("{models|datasets}/{ns}/{name}",
	// docs/repo-transfer-design.md §4). storage.LegacyStoragePath(kind, ns,
	// repo) produces the same value; it is spelled out here so it stays a
	// compile-time constant.
	storagePath = "datasets/" + ns + "/" + repo

	zeroHash = "0000000000000000000000000000000000000000"
	hashA    = "1111111111111111111111111111111111111111"
	hashB    = "2222222222222222222222222222222222222222"
	hashC    = "3333333333333333333333333333333333333333"
)

func mustMarshalIndex(t testingT, ix *Index) []byte {
	t.Helper()
	body, err := json.Marshal(ix)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	return body
}

func readIndexOrFail(t *testing.T, st storage.Storage) (*Index, int64) {
	t.Helper()
	ix, gen, err := ReadIndex(context.Background(), st, storagePath)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	return ix, gen
}

// seedIndex puts a repository into a known state without going through the CAS
// path, so tests start from the situation they care about.
func seedIndex(t *testing.T, f *fakeStore, refs map[string]string, entries []string, seq int) {
	t.Helper()
	f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs = refs
		ix.Entries = entries
		ix.Seq = seq
	})
}

func TestReadIndex_MissingObjectIsEmptyAtGenerationZero(t *testing.T) {
	f := newFakeStore()
	ix, gen := readIndexOrFail(t, f)
	if gen != 0 {
		t.Errorf("generation = %d, want 0 (drives the DoesNotExist precondition)", gen)
	}
	if len(ix.Refs) != 0 || len(ix.Entries) != 0 || ix.Seq != 0 {
		t.Errorf("empty index = %+v, want zero values", ix)
	}
}

func TestReadIndex_RejectsNewerSchema(t *testing.T) {
	f := newFakeStore()
	f.writeIndexUnconditionally(t, storagePath, func(ix *Index) { ix.Version = IndexVersion + 1 })
	if _, _, err := ReadIndex(context.Background(), f, storagePath); !errors.Is(err, ErrIndexVersion) {
		t.Fatalf("ReadIndex error = %v, want ErrIndexVersion", err)
	}
}

func TestReadIndex_CorruptJSONIsAnError(t *testing.T) {
	f := newFakeStore()
	if err := f.Put(context.Background(), storage.WALIndexKey(storagePath), strings.NewReader("{not json"), "application/json"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A damaged index must never read as "empty repository": that would let a
	// push wipe every ref (§13, index corruption is a single point of failure).
	if _, _, err := ReadIndex(context.Background(), f, storagePath); err == nil {
		t.Fatal("ReadIndex on corrupt JSON returned no error")
	}
}

func TestUpdateIndex_FirstPushCreatesIndexAtGenerationZero(t *testing.T) {
	f := newFakeStore()
	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: zeroHash, New: hashA}}, "entries/000001-X.pack")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	ix, gen := readIndexOrFail(t, f)
	if gen == 0 {
		t.Error("generation still 0 after create")
	}
	if ix.Refs["refs/heads/main"] != hashA {
		t.Errorf("refs = %v, want main=%s", ix.Refs, hashA)
	}
	if ix.Seq != 1 || len(ix.Entries) != 1 || ix.Entries[0] != "entries/000001-X.pack" {
		t.Errorf("seq=%d entries=%v, want seq=1 with the uploaded entry", ix.Seq, ix.Entries)
	}
	if ix.Version != IndexVersion {
		t.Errorf("version = %d, want %d", ix.Version, IndexVersion)
	}
	if ix.UpdatedAt.IsZero() {
		t.Error("updated_at not stamped")
	}
}

func TestUpdateIndex_FirstPushRacesAnotherFirstPushOnAnotherRef(t *testing.T) {
	f := newFakeStore()
	// Both writers see "no index". The loser of the create retries and finds
	// its precondition (main absent) still holds.
	f.beforePut = func(attempt int) {
		if attempt != 1 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/other"] = hashC
			ix.Entries = append(ix.Entries, "entries/000001-OTHER.pack")
			ix.Seq = 1
		})
	}
	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: "", New: hashA}}, "entries/000001-MINE.pack")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashA || ix.Refs["refs/heads/other"] != hashC {
		t.Errorf("refs = %v, want both branches present", ix.Refs)
	}
	if got := ix.Entries; len(got) != 2 || got[0] != "entries/000001-OTHER.pack" || got[1] != "entries/000001-MINE.pack" {
		t.Errorf("entries = %v, want the winner's entry then ours, in order", got)
	}
	if ix.Seq != 2 {
		t.Errorf("seq = %d, want 2", ix.Seq)
	}
}

func TestUpdateIndex_FirstPushRacesAnotherFirstPushOnTheSameRef(t *testing.T) {
	f := newFakeStore()
	// Two clients create refs/heads/main at once. Both sent the zero hash as
	// <old>; the loser must be told to fetch, not silently overwrite.
	f.beforePut = func(attempt int) {
		if attempt != 1 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/main"] = hashC
		})
	}
	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: zeroHash, New: hashA}}, "entries/000001-X.pack")

	var stale *StaleRefError
	if !errors.As(err, &stale) {
		t.Fatalf("UpdateIndex error = %v, want StaleRefError", err)
	}
	if stale.Ref != "refs/heads/main" || stale.Actual != hashC {
		t.Errorf("stale = %+v, want ref=refs/heads/main actual=%s", stale, hashC)
	}
	if !errors.Is(err, ErrStaleRef) {
		t.Error("errors.Is(err, ErrStaleRef) = false")
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashC {
		t.Errorf("the winner's ref was overwritten: %v", ix.Refs)
	}
}

func TestUpdateIndex_ConcurrentPushToAnotherRefRetriesAndSucceeds(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA}, []string{"entries/000001-A.pack"}, 1)

	f.beforePut = func(attempt int) {
		if attempt != 1 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/feature"] = hashC
			ix.Entries = append(ix.Entries, "entries/000002-OTHER.pack")
			ix.Seq = 2
		})
	}

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: hashA, New: hashB}}, "entries/000002-MINE.pack")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	if f.casCalls != 2 {
		t.Errorf("CAS attempts = %d, want 2 (one loss, one win)", f.casCalls)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashB || ix.Refs["refs/heads/feature"] != hashC {
		t.Errorf("refs = %v, want main=%s feature=%s", ix.Refs, hashB, hashC)
	}
	want := []string{"entries/000001-A.pack", "entries/000002-OTHER.pack", "entries/000002-MINE.pack"}
	if got := ix.Entries; len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if ix.Entries[i] != want[i] {
			t.Fatalf("entries = %v, want %v (order is meaning)", ix.Entries, want)
		}
	}
	if ix.Seq != 3 {
		t.Errorf("seq = %d, want 3", ix.Seq)
	}
}

func TestUpdateIndex_ConcurrentPushToTheSameRefIsStale(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA}, nil, 0)

	f.beforePut = func(attempt int) {
		if attempt != 1 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/main"] = hashC
		})
	}

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: hashA, New: hashB}}, "entries/000001-MINE.pack")

	var stale *StaleRefError
	if !errors.As(err, &stale) {
		t.Fatalf("UpdateIndex error = %v, want StaleRefError", err)
	}
	if stale.Ref != "refs/heads/main" || stale.Expected != hashA || stale.Actual != hashC {
		t.Errorf("stale = %+v", stale)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashC {
		t.Errorf("non-fast-forward overwrite happened: refs = %v", ix.Refs)
	}
	if len(ix.Entries) != 0 {
		t.Errorf("entries = %v, want the rejected push to leave the index alone", ix.Entries)
	}
}

func TestUpdateIndex_SurvivesSeveralRoundsOfUnrelatedConflicts(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA}, nil, 0)

	// Lose three times in a row to unrelated refs, win on the fourth attempt:
	// one attempt short of the cap, so the boundary is exercised from below.
	f.beforePut = func(attempt int) {
		if attempt > 3 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/other"] = hashC
			ix.Seq++
		})
	}

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: hashA, New: hashB}}, "entries/000009-MINE.pack")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	if f.casCalls != 4 {
		t.Errorf("CAS attempts = %d, want 4", f.casCalls)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashB {
		t.Errorf("refs = %v", ix.Refs)
	}
	// The entry must be appended exactly once no matter how many attempts ran:
	// each attempt restarts from a freshly read index.
	count := 0
	for _, e := range ix.Entries {
		if e == "entries/000009-MINE.pack" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("entry appended %d times, want exactly 1 (entries = %v)", count, ix.Entries)
	}
}

func TestUpdateIndex_GivesUpAfterTheRetryCap(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA}, nil, 0)

	// A writer that never stops: every attempt loses to an unrelated ref.
	f.beforePut = func(int) {
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/other"] = hashC
			ix.Seq++
		})
	}

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: hashA, New: hashB}}, "entries/000001-MINE.pack")
	if !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("UpdateIndex error = %v, want ErrRetryExhausted", err)
	}
	if errors.Is(err, ErrStaleRef) {
		t.Error("exhaustion must not masquerade as a stale ref: the update is still valid")
	}
	if f.casCalls != maxCASAttempts {
		t.Errorf("CAS attempts = %d, want %d", f.casCalls, maxCASAttempts)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashA {
		t.Errorf("refs = %v, want main untouched", ix.Refs)
	}
}

func TestUpdateIndex_StaleBeforeAnyConditionalWrite(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashC}, nil, 0)

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: hashA, New: hashB}}, "")
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("UpdateIndex error = %v, want ErrStaleRef", err)
	}
	if f.casCalls != 0 {
		t.Errorf("CAS attempts = %d, want 0: the precondition is checked before writing", f.casCalls)
	}
}

func TestUpdateIndex_DeleteAndUpdateInOneCall(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{
		"refs/heads/main": hashA,
		"refs/tags/v1":    hashC,
	}, nil, 0)

	err := UpdateIndex(context.Background(), f, storagePath, []RefUpdate{
		{Ref: "refs/heads/main", Old: hashA, New: hashB},
		{Ref: "refs/tags/v1", Old: hashC, New: zeroHash},
	}, "entries/000001-MINE.pack")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashB {
		t.Errorf("main = %s, want %s", ix.Refs["refs/heads/main"], hashB)
	}
	if _, ok := ix.Refs["refs/tags/v1"]; ok {
		t.Errorf("deleted ref still present: %v", ix.Refs)
	}
	if ix.Seq != 1 || len(ix.Entries) != 1 {
		t.Errorf("seq=%d entries=%v, want one entry for the whole batch", ix.Seq, ix.Entries)
	}
}

func TestUpdateIndex_DeleteRacesWithAnUpdateToTheSameRef(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/topic": hashA}, nil, 0)

	// We want to delete topic at hashA; somebody advances it first. Deleting
	// what we have not seen would throw their commit away.
	f.beforePut = func(attempt int) {
		if attempt != 1 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/topic"] = hashC
		})
	}

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/topic", Old: hashA, New: zeroHash}}, "")
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("UpdateIndex error = %v, want ErrStaleRef", err)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/topic"] != hashC {
		t.Errorf("refs = %v, want the concurrent update to survive", ix.Refs)
	}
}

func TestUpdateIndex_UpdateRacesWithADeleteOfTheSameRef(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/topic": hashA}, nil, 0)

	f.beforePut = func(attempt int) {
		if attempt != 1 {
			return
		}
		f.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			delete(ix.Refs, "refs/heads/topic")
		})
	}

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/topic", Old: hashA, New: hashB}}, "entries/000001-MINE.pack")

	var stale *StaleRefError
	if !errors.As(err, &stale) {
		t.Fatalf("UpdateIndex error = %v, want StaleRefError", err)
	}
	if stale.Actual != "" {
		t.Errorf("stale.Actual = %q, want empty (the ref is gone)", stale.Actual)
	}
	if !strings.Contains(stale.Error(), "<absent>") {
		t.Errorf("message %q should say the ref is absent", stale.Error())
	}
}

func TestUpdateIndex_DeleteOfAnAlreadyMissingRefIsStale(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA}, nil, 0)

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/gone", Old: hashB, New: zeroHash}}, "")
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("UpdateIndex error = %v, want ErrStaleRef", err)
	}
}

func TestUpdateIndex_RefOnlyUpdateLeavesSeqAlone(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA, "refs/heads/dead": hashB}, []string{"entries/000007-A.pack"}, 7)

	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/dead", Old: hashB, New: ""}}, "")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Seq != 7 {
		t.Errorf("seq = %d, want 7: seq numbers entries, not index revisions", ix.Seq)
	}
	if len(ix.Entries) != 1 {
		t.Errorf("entries = %v, want unchanged", ix.Entries)
	}
}

func TestUpdateIndex_ZeroHashAndEmptyStringAreTheSameAbsence(t *testing.T) {
	f := newFakeStore()
	// Old given as the zero hash against an index where the ref is absent.
	if err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: zeroHash, New: hashA}}, ""); err != nil {
		t.Fatalf("zero-hash create: %v", err)
	}
	// New given as the empty string deletes, exactly like the zero hash.
	if err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: hashA, New: ""}}, ""); err != nil {
		t.Fatalf("empty-string delete: %v", err)
	}
	ix, _ := readIndexOrFail(t, f)
	if len(ix.Refs) != 0 {
		t.Errorf("refs = %v, want empty", ix.Refs)
	}
}

func TestUpdateIndex_MixedCaseHashesCompareEqual(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": "AABBCCDDEEFF00112233445566778899AABBCCDD"}, nil, 0)
	err := UpdateIndex(context.Background(), f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: "aabbccddeeff00112233445566778899aabbccdd", New: hashB}}, "")
	if err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
}

func TestPutIndex_RefusesToOverwriteADifferentGeneration(t *testing.T) {
	f := newFakeStore()
	seedIndex(t, f, map[string]string{"refs/heads/main": hashA}, nil, 0)
	_, gen := readIndexOrFail(t, f)

	seedIndex(t, f, map[string]string{"refs/heads/main": hashB}, nil, 0) // generation moves

	_, err := PutIndex(context.Background(), f, storagePath, gen, NewIndex())
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("PutIndex error = %v, want ErrPreconditionFailed", err)
	}
	ix, _ := readIndexOrFail(t, f)
	if ix.Refs["refs/heads/main"] != hashB {
		t.Errorf("refs = %v, want the newer write to stand", ix.Refs)
	}
}

func TestPutIndex_GenerationZeroOnlyCreates(t *testing.T) {
	f := newFakeStore()
	if _, err := PutIndex(context.Background(), f, storagePath, 0, NewIndex()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := PutIndex(context.Background(), f, storagePath, 0, NewIndex()); !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("second create error = %v, want ErrPreconditionFailed", err)
	}
}

func TestIndexClone_DoesNotShareRefsWithTheOriginal(t *testing.T) {
	original := NewIndex()
	original.Refs["refs/heads/main"] = hashA
	original.Entries = []string{"entries/000001-A.pack"}

	clone := original.clone()
	clone.Refs["refs/heads/main"] = hashB
	clone.Entries = append(clone.Entries, "entries/000002-B.pack")

	if original.Refs["refs/heads/main"] != hashA {
		t.Error("clone aliased the refs map: a failed CAS would corrupt the retry")
	}
	if len(original.Entries) != 1 {
		t.Error("clone aliased the entries slice")
	}
}

func TestNeedsCompaction_TriggersAboveThreshold(t *testing.T) {
	ix := NewIndex()
	ix.Entries = make([]string, 3)
	if NeedsCompaction(ix, 3) {
		t.Error("threshold is exclusive: 3 entries with a limit of 3 should not compact")
	}
	ix.Entries = make([]string, 4)
	if !NeedsCompaction(ix, 3) {
		t.Error("4 entries with a limit of 3 should compact")
	}
	if NeedsCompaction(ix, 0) != (4 > DefaultCompactionThreshold) {
		t.Error("a non-positive limit should fall back to the default")
	}
}

func TestEntryName_ZeroPadsSeqForLexicalOrdering(t *testing.T) {
	if got, want := EntryName(42, "01JAV"), "entries/000042-01JAV.pack"; got != want {
		t.Errorf("EntryName = %q, want %q", got, want)
	}
	if got, want := BaseName("01JAV"), "base/01JAV.pack"; got != want {
		t.Errorf("BaseName = %q, want %q", got, want)
	}
	// Six digits is a format, not a limit: larger sequences must still work.
	if got := EntryName(1234567, "X"); got != "entries/1234567-X.pack" {
		t.Errorf("EntryName(1234567) = %q", got)
	}
}

// crockfordAlphabet mirrors the unexported alphabet internal/ulid.New()
// encodes with; it lives here only so this format-conformance test does not
// need to reach into that package's internals.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func TestNewULID_IsUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newULID()
		if len(id) != 26 {
			t.Fatalf("ULID %q has length %d, want 26", id, len(id))
		}
		for _, c := range id {
			if !strings.ContainsRune(crockfordAlphabet, c) {
				t.Fatalf("ULID %q contains %q, outside the Crockford alphabet", id, c)
			}
		}
		if seen[id] {
			t.Fatalf("duplicate ULID %q: WAL entry keys would collide", id)
		}
		seen[id] = true
	}
}

func TestIsAbsent(t *testing.T) {
	tests := []struct {
		hash string
		want bool
	}{
		{"", true},
		{zeroHash, true},
		{strings.Repeat("0", 64), true}, // sha256 repositories
		{hashA, false},
		{"0000000000000000000000000000000000000001", false},
	}
	for _, tt := range tests {
		if got := isAbsent(tt.hash); got != tt.want {
			t.Errorf("isAbsent(%q) = %v, want %v", tt.hash, got, tt.want)
		}
	}
}
