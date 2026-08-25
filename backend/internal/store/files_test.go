package store

import (
	"context"
	"errors"
	"testing"
)

func TestRecordLFSObject_RequiresConfirmPresent(t *testing.T) {
	s := &Store{}
	err := s.RecordLFSObject(context.Background(), 1, "oid", 1, nil)
	if err == nil {
		t.Fatal("RecordLFSObject(nil confirmPresent): want an error")
	}
}

// confirmPresent is a round trip to object storage made with a write
// transaction open, and on SQLite that transaction owns the process's only
// writer connection -- so every push, login and repository creation queues
// behind it, and another process waits out sqliteBusyTimeout and gets
// SQLITE_BUSY. It has to stay inside the transaction for the oids that are
// actually racing the collector, but a repository re-pushing an object it
// already links to is not one of them: neither GC pass can touch a referenced
// object, so there is nothing to hold a lock against and both writes would be
// no-ops. That call takes no transaction at all now, which this pins by
// answering it with a confirmPresent that would fail the check.
func TestIntegrationRecordLFSObjectSkipsStorageForALinkedObject(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		mine := f.repo(t, "alice", "mine", "model", nil)
		theirs := f.repo(t, "bob", "theirs", "model", nil)

		const oid = "oid-linked"
		if err := s.RecordLFSObject(ctx, mine.ID, oid, 42, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("first record: %v", err)
		}

		calls := 0
		gone := func(string) (bool, error) {
			calls++
			return false, nil
		}
		if err := s.RecordLFSObject(ctx, mine.ID, oid, 42, gone); err != nil {
			t.Fatalf("re-record of a linked object = %v, want nil", err)
		}
		if calls != 0 {
			t.Errorf("confirmPresent ran %d times for an already linked object, want 0", calls)
		}

		// The shortcut is per link, not per oid: another repository is
		// claiming a share of these bytes for the first time, which is
		// exactly the case the collector can be racing, so it still confirms
		// them under the row lock.
		if err := s.RecordLFSObject(ctx, theirs.ID, oid, 42, gone); !errors.Is(err, ErrLFSObjectGone) {
			t.Fatalf("record for a second repository = %v, want ErrLFSObjectGone", err)
		}
		if calls != 1 {
			t.Errorf("confirmPresent ran %d times for a new link, want 1", calls)
		}
		if has, err := s.RepoHasLFSObject(ctx, theirs.ID, oid); err != nil || has {
			t.Errorf("the rolled back record left a link: %v, %v", has, err)
		}
		// The first repository's own link is untouched by that rollback.
		if has, err := s.RepoHasLFSObject(ctx, mine.ID, oid); err != nil || !has {
			t.Errorf("the original link is gone: %v, %v", has, err)
		}
	})
}
