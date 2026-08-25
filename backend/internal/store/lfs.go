package store

import "context"

// RepoHasLFSObject reports whether oid is linked to this repository in
// repo_lfs_objects.
//
// LFS bytes are content-addressed and shared instance-wide
// (storage.LFSKey(oid) has no repository in it), so "may the caller read some
// repository" is not an authorisation for an object: knowing an oid would
// otherwise be enough to pull another repository's bytes through a
// repository of one's own. Every legitimate producer of an object records the
// link first -- the LFS batch upload dedup branch, verify, the emulator proxy
// upload (all via RecordLFSObject), the HF-compatible commit handler and the
// syncer's post-push pipeline (both via LinkLFSObjects) -- so membership is
// the right question to ask before handing out a download.
//
// The syncer is the one that catches a pointer committed as an ordinary blob,
// by a client that never spoke the LFS protocol at all; without it a plain
// `git push` of pointer text left repo_files naming an oid this predicate
// then denied, and left `thinkingface gc` counting the bytes as unreferenced.
func (s *Store) RepoHasLFSObject(ctx context.Context, repoID int64, oid string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM repo_lfs_objects WHERE repo_id = $1 AND oid = $2)`,
		repoID, oid).Scan(&exists)
	return exists, err
}
