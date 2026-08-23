package lfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

const goodOID = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"

func TestValidOID(t *testing.T) {
	tests := []struct {
		name string
		oid  string
		want bool
	}{
		{"sha256 hex digest", goodOID, true},
		{"empty", "", false},
		{"too short", strings.Repeat("a", 63), false},
		{"too long", strings.Repeat("a", 65), false},
		{"uppercase", strings.ToUpper(goodOID), false},
		{"non-hex", strings.Repeat("z", 64), false},
		{"path traversal", "../../wal/models/victim/repo/index.json", false},
		{"sha256 prefix left on", "sha256:" + goodOID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidOID(tt.oid); got != tt.want {
				t.Fatalf("ValidOID(%q) = %v, want %v", tt.oid, got, tt.want)
			}
		})
	}
}

// A batch naming an oid that is not a digest must be refused per-object,
// before the value reaches storage.LFSKey, where it would otherwise name an
// object key outside the content-addressed lfs/ prefix. The nil store and
// storage here are the assertion: reaching either one would panic.
func TestBatchRejectsMalformedOIDWithoutTouchingStorage(t *testing.T) {
	h := New(nil, nil, 0, "http://localhost:8080", "secret")

	for _, op := range []string{"upload", "download"} {
		resp, err := h.Batch(context.Background(), 1, &BatchRequest{
			Operation: op,
			Objects: []ObjectRef{
				{OID: "../../wal/models/victim/repo/index.json", Size: 10},
				{OID: goodOID, Size: -1},
			},
		}, "")
		if err != nil {
			t.Fatalf("Batch(%s) returned an error: %v", op, err)
		}
		if len(resp.Objects) != 2 {
			t.Fatalf("Batch(%s) returned %d objects, want 2", op, len(resp.Objects))
		}
		for i, obj := range resp.Objects {
			if obj.Error == nil {
				t.Errorf("Batch(%s) object %d was accepted, want a validation error", op, i)
			}
			if obj.Actions != nil {
				t.Errorf("Batch(%s) object %d was handed transfer actions", op, i)
			}
		}
	}
}

func TestBatchRejectsUnknownOperation(t *testing.T) {
	h := New(nil, nil, 0, "http://localhost:8080", "secret")
	_, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "delete",
		Objects:   []ObjectRef{{OID: goodOID, Size: 1}},
	}, "")
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("Batch error = %v, want ErrUnsupportedOperation", err)
	}
}

func TestVerifyProxySignature(t *testing.T) {
	h := New(nil, nil, 0, "http://localhost:8080", "secret")
	href := h.proxyHref("upload", 7, goodOID)

	_, query, _ := strings.Cut(href, "?")
	params := map[string]string{}
	for _, pair := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(pair, "=")
		params[k] = v
	}

	if !h.VerifyProxySignature("upload", 7, goodOID, params["exp"], params["sig"]) {
		t.Fatal("the signature this handler issued did not verify")
	}
	// Every field is covered by the MAC, so changing any one of them must fail.
	if h.VerifyProxySignature("download", 7, goodOID, params["exp"], params["sig"]) {
		t.Error("a signature issued for upload verified as download")
	}
	if h.VerifyProxySignature("upload", 8, goodOID, params["exp"], params["sig"]) {
		t.Error("a signature issued for repo 7 verified for repo 8")
	}
	if h.VerifyProxySignature("upload", 7, strings.Repeat("a", 64), params["exp"], params["sig"]) {
		t.Error("a signature issued for one oid verified for another")
	}
	if h.VerifyProxySignature("upload", 7, goodOID, params["exp"], "") {
		t.Error("an empty signature verified")
	}
	if h.VerifyProxySignature("upload", 7, goodOID, "1", params["sig"]) {
		t.Error("an expired signature verified")
	}
}

// fakeRecorder implements lfsRecorder the way Store does: it calls
// confirmPresent under the "row lock" and returns ErrLFSObjectGone if
// storage is gone, so Batch/Verify can be tested without Postgres.
type fakeRecorder struct {
	calls int
	// owned is the set of (repoID, oid) links the store would have. Empty
	// means the repository owns nothing, which is what an outsider guessing
	// an oid looks like.
	owned    map[string]bool
	ownCalls int
}

func (f *fakeRecorder) RepoHasLFSObject(_ context.Context, repoID int64, oid string) (bool, error) {
	f.ownCalls++
	return f.owned[fmt.Sprintf("%d/%s", repoID, oid)], nil
}

func ownedBy(repoID int64, oid string) map[string]bool {
	return map[string]bool{fmt.Sprintf("%d/%s", repoID, oid): true}
}

// RecordLFSObject mirrors the real store closely enough for these tests: it
// calls confirmPresent under the "row lock" with the object's one and only
// key, and reports ErrLFSObjectGone when the bytes turn out to be gone.
func (f *fakeRecorder) RecordLFSObject(_ context.Context, _ int64, oid string, _ int64, confirmPresent func(key string) (bool, error)) error {
	f.calls++
	if confirmPresent == nil {
		return errors.New("confirmPresent is required")
	}
	ok, err := confirmPresent(storage.LFSKey(oid))
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrLFSObjectGone
	}
	return nil
}

// stubStorage is enough of storage.Storage for Batch/Verify: Stat plus the
// unsigned-URL upload path. presentFor is how many Stat calls return a hit
// (0 means the object is missing from the start).
type stubStorage struct {
	nStat      int
	presentFor int
	size       int64
}

func (s *stubStorage) SupportsSignedURL() bool { return false }
func (s *stubStorage) SignedGetURL(context.Context, string, time.Duration, string) (string, error) {
	return "", errors.New("signed URLs not supported")
}
func (s *stubStorage) SignedPutURL(context.Context, string, time.Duration, int64) (string, error) {
	return "", errors.New("signed URLs not supported")
}
func (s *stubStorage) Put(context.Context, string, io.Reader, string) error {
	return errors.New("unused")
}
func (s *stubStorage) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}
func (s *stubStorage) GetWithGeneration(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("unused")
}
func (s *stubStorage) PutIfGeneration(context.Context, string, int64, io.Reader, string) (int64, error) {
	return 0, errors.New("unused")
}
func (s *stubStorage) GetRange(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}
func (s *stubStorage) Stat(context.Context, string) (storage.ObjectInfo, error) {
	s.nStat++
	if s.nStat <= s.presentFor {
		return storage.ObjectInfo{Size: s.size}, nil
	}
	return storage.ObjectInfo{}, storage.ErrNotFound
}
func (s *stubStorage) Copy(context.Context, string, string) error { return errors.New("unused") }
func (s *stubStorage) Delete(context.Context, string) error       { return errors.New("unused") }
func (s *stubStorage) List(context.Context, string) ([]storage.ObjectInfo, error) {
	return nil, errors.New("unused")
}
func (s *stubStorage) PublicURI(string) string { return "" }

var _ storage.Storage = (*stubStorage)(nil)
var _ lfsRecorder = (*fakeRecorder)(nil)

func testHandler(rec lfsRecorder, st storage.Storage) *Handler {
	return &Handler{
		store:     rec,
		storage:   st,
		ttl:       time.Hour,
		publicURL: "http://localhost:8080",
		secret:    []byte("secret"),
	}
}

func TestBatchUploadOmitsActionsWhenObjectStillPresent(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 2, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
	if len(resp.Objects) != 1 || resp.Objects[0].Actions != nil || resp.Objects[0].Error != nil {
		t.Fatalf("object = %+v, want a deduplicated hit with no actions", resp.Objects[0])
	}
}

func TestBatchUploadIssuesActionWhenGCDeletedDuringRecord(t *testing.T) {
	// First Stat (the batch existence check) hits; the confirmPresent Stat
	// inside RecordLFSObject misses — GC held FOR UPDATE, deleted the
	// bytes, then dropped the row.
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 1, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
	if len(resp.Objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(resp.Objects))
	}
	if _, ok := resp.Objects[0].Actions["upload"]; !ok {
		t.Fatalf("actions = %v, want an upload action so the client re-uploads", resp.Objects[0].Actions)
	}
}

func TestBatchUploadIssuesActionWhenObjectMissing(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 0, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("RecordLFSObject calls = %d, want 0 for a miss", rec.calls)
	}
	if _, ok := resp.Objects[0].Actions["upload"]; !ok {
		t.Fatalf("actions = %v, want an upload action", resp.Objects[0].Actions)
	}
}

// Download must be gated on repository membership, not on the object merely
// existing in the bucket: LFS keys are content addresses with no repository
// in them, so presence alone would hand any oid to anyone who can read any
// repository.
func TestBatchDownloadRefusesObjectNotLinkedToRepo(t *testing.T) {
	rec := &fakeRecorder{} // no links at all
	st := &stubStorage{presentFor: 5, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "download",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if len(resp.Objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(resp.Objects))
	}
	obj := resp.Objects[0]
	if obj.Actions != nil {
		t.Fatalf("actions = %v, want none for an object this repository does not own", obj.Actions)
	}
	if obj.Error == nil || obj.Error.Code != 404 {
		t.Fatalf("error = %+v, want a per-object 404", obj.Error)
	}
	// The bucket must not even be consulted: the answer cannot depend on
	// whether some other repository uploaded these bytes.
	if st.nStat != 0 {
		t.Errorf("storage.Stat calls = %d, want 0 for an unowned oid", st.nStat)
	}
}

func TestBatchDownloadAllowsObjectLinkedToRepo(t *testing.T) {
	rec := &fakeRecorder{owned: ownedBy(1, goodOID)}
	st := &stubStorage{presentFor: 5, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "download",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error != nil {
		t.Fatalf("error = %+v, want none", resp.Objects[0].Error)
	}
	if _, ok := resp.Objects[0].Actions["download"]; !ok {
		t.Fatalf("actions = %v, want a download action", resp.Objects[0].Actions)
	}
}

// A link that outlived its bytes (GC between push and fetch) is still a 404,
// not a signed URL to nothing.
func TestBatchDownloadRefusesLinkedButMissingObject(t *testing.T) {
	rec := &fakeRecorder{owned: ownedBy(1, goodOID)}
	st := &stubStorage{presentFor: 0, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "download",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error == nil || resp.Objects[0].Error.Code != 404 {
		t.Fatalf("error = %+v, want a per-object 404", resp.Objects[0].Error)
	}
}

// Upload dedup is what creates the link in the first place, so it must not
// start depending on one.
func TestBatchUploadDedupDoesNotRequireExistingLink(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 2, size: 10}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if rec.ownCalls != 0 {
		t.Errorf("RepoHasLFSObject calls on upload = %d, want 0", rec.ownCalls)
	}
	if resp.Objects[0].Actions != nil || resp.Objects[0].Error != nil {
		t.Fatalf("object = %+v, want a deduplicated hit with no actions", resp.Objects[0])
	}
}

func TestVerifyFailsWhenGCDeletedDuringRecord(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 1, size: 10}
	h := testHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, 10)
	if err == nil || !strings.Contains(err.Error(), "was not uploaded") {
		t.Fatalf("Verify error = %v, want 'was not uploaded'", err)
	}
	if rec.calls != 1 {
		t.Fatalf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
}

func TestVerifySucceedsWhenObjectStillPresent(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 2, size: 10}
	h := testHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, 10); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
}
