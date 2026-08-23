package storage

import "strings"

// LFSKey returns the content-addressed key for an LFS object. This layer is the
// source of truth: immutable, deduplicated across every repository.
func LFSKey(oid string) string {
	if len(oid) < 4 {
		return "lfs/" + oid
	}
	return "lfs/" + oid[0:2] + "/" + oid[2:4] + "/" + oid
}

// BlobKey returns the content-addressed key for a non-LFS git blob: the
// bytes of every plain file on a pushed ref are published here so
// `gcloud storage cp` can fetch them next to the LFS objects. Immutable and
// deduplicated like LFSKey; `thinkingface gc` collects blobs no repo_files
// row references any more.
func BlobKey(sha string) string {
	if len(sha) < 4 {
		return "blobs/" + sha
	}
	return "blobs/" + sha[0:2] + "/" + sha[2:4] + "/" + sha
}

func kindDir(kind string) string {
	if kind == "model" {
		return "models"
	}
	return "datasets"
}

// WALPrefix returns the directory key (with trailing slash) that holds one
// repository's write-ahead log (docs/dev/continuity-design.md §3). It is keyed by
// the repository's immutable storage path, not its name, so transferring or
// renaming a repository never relocates the WAL
// (docs/dev/repo-transfer-design.md §3).
//
//	wal/{storage_path}/            e.g. wal/repos/01J…/ or (legacy) wal/datasets/{ns}/{name}/
func WALPrefix(storagePath string) string {
	return "wal/" + strings.Trim(storagePath, "/") + "/"
}

// WALIndexKey is the single CAS target per repository: the object whose
// generation *is* the repository version (§4).
func WALIndexKey(storagePath string) string {
	return WALPrefix(storagePath) + "index.json"
}

// WALBasePrefix holds compaction output: one self-contained pack per compaction.
func WALBasePrefix(storagePath string) string {
	return WALPrefix(storagePath) + "base/"
}

// WALEntriesPrefix holds one pack per push, applied in index order.
func WALEntriesPrefix(storagePath string) string {
	return WALPrefix(storagePath) + "entries/"
}

// WALKey resolves a name stored inside the index ("entries/000042-….pack",
// "base/….pack") to a full storage key. The index deliberately stores
// repository-relative names so the WAL can be relocated wholesale.
func WALKey(storagePath, rel string) string {
	return WALPrefix(storagePath) + strings.TrimPrefix(rel, "/")
}

// LegacyStoragePath is the storage path a repository created before
// storage_path existed was backfilled with: its then-current physical
// location, "{models|datasets}/{ns}/{name}". Tests and migrations use it;
// new repositories get store.NewStoragePath().
func LegacyStoragePath(kind, namespace, name string) string {
	return kindDir(kind) + "/" + namespace + "/" + name
}
