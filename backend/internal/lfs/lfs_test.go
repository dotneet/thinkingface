package lfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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
	h := New(nil, nil, 0, 0, "http://localhost:8080", "secret")

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
	h := New(nil, nil, 0, 0, "http://localhost:8080", "secret")
	_, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "delete",
		Objects:   []ObjectRef{{OID: goodOID, Size: 1}},
	}, "")
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("Batch error = %v, want ErrUnsupportedOperation", err)
	}
}

func TestVerifyProxySignature(t *testing.T) {
	h := New(nil, nil, 0, 0, "http://localhost:8080", "secret")
	href := h.proxyHref("upload", 7, goodOID, time.Hour)

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
	// log, when set, is shared with keyStorage so a test can assert the order
	// of storage and store operations against each other.
	log *opLog
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
func (f *fakeRecorder) RecordLFSObject(_ context.Context, repoID int64, oid string, _ int64, confirmPresent func(key string) (bool, error)) error {
	f.calls++
	f.log.add("record")
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
	// A successful record is a link, and the link is what a later verify
	// reads back as proof this repository uploaded the object.
	if f.owned == nil {
		f.owned = map[string]bool{}
	}
	f.owned[fmt.Sprintf("%d/%s", repoID, oid)] = true
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
func (s *stubStorage) SignedPutURL(context.Context, string, time.Duration) (string, error) {
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
func (s *stubStorage) Copy(context.Context, string, string) error { return nil }
func (s *stubStorage) Delete(context.Context, string) error       { return nil }
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
		maxTTL:    12 * time.Hour,
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

// opLog records the order of operations across the storage stub and the store
// fake. Promotion order is the whole point of the staging design, so the tests
// assert it rather than just the end state.
type opLog struct{ ops []string }

func (l *opLog) add(op string) {
	if l != nil {
		l.ops = append(l.ops, op)
	}
}

// keyStorage is stubStorage's sibling for tests that care about *which* keys
// are touched: a tiny key -> size namespace instead of a Stat call counter.
// Everything it does not override is inherited from stubStorage and panics
// loudly enough (an error) if a test reaches it.
type keyStorage struct {
	*stubStorage
	signing bool
	objects map[string]int64
	log     *opLog
	// signedPuts records every key a PUT was signed for, in order.
	signedPuts []string
}

func newKeyStorage(objects map[string]int64) *keyStorage {
	if objects == nil {
		objects = map[string]int64{}
	}
	return &keyStorage{stubStorage: &stubStorage{}, objects: objects, log: &opLog{}}
}

func (k *keyStorage) SupportsSignedURL() bool { return k.signing }

func (k *keyStorage) SignedPutURL(_ context.Context, key string, _ time.Duration) (string, error) {
	if !k.signing {
		return "", errors.New("signed URLs not supported")
	}
	k.signedPuts = append(k.signedPuts, key)
	return "https://signed.example/" + key, nil
}

func (k *keyStorage) SignedGetURL(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
	if !k.signing {
		return "", errors.New("signed URLs not supported")
	}
	return "https://signed.example/" + key, nil
}

func (k *keyStorage) Stat(_ context.Context, key string) (storage.ObjectInfo, error) {
	k.log.add("stat " + key)
	size, ok := k.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Key: key, Size: size}, nil
}

func (k *keyStorage) Copy(_ context.Context, srcKey, dstKey string) error {
	k.log.add("copy " + srcKey + " -> " + dstKey)
	size, ok := k.objects[srcKey]
	if !ok {
		return storage.ErrNotFound
	}
	k.objects[dstKey] = size
	return nil
}

func (k *keyStorage) Delete(_ context.Context, key string) error {
	k.log.add("delete " + key)
	delete(k.objects, key)
	return nil
}

var _ storage.Storage = (*keyStorage)(nil)

func keyTestHandler(rec *fakeRecorder, st *keyStorage) *Handler {
	rec.log = st.log
	h := testHandler(rec, st)
	return h
}

func TestTTLFor(t *testing.T) {
	const (
		base = time.Hour
		max  = 12 * time.Hour
		gib  = int64(1) << 30
	)
	tests := []struct {
		name      string
		base, max time.Duration
		n         int64
		want      time.Duration
	}{
		{name: "zero bytes gets the base lifetime", base: base, max: max, n: 0, want: base},
		{name: "unknown size gets the base lifetime", base: base, max: max, n: -1, want: base},
		{name: "one MiB adds a second", base: base, max: max, n: 1 << 20, want: base + time.Second},
		{name: "sub-MiB rounds down to nothing", base: base, max: max, n: 4096, want: base},
		{name: "1 GiB", base: base, max: max, n: gib, want: base + 1024*time.Second},
		{name: "10 GiB", base: base, max: max, n: 10 * gib, want: base + 10240*time.Second},
		{name: "100 GiB hits the ceiling", base: base, max: max, n: 100 * gib, want: max},
		{name: "MaxInt64 bytes cannot overflow past the ceiling", base: base, max: max, n: math.MaxInt64, want: max},
		{name: "no ceiling configured leaves the sum alone", base: base, max: 0, n: gib, want: base + 1024*time.Second},
		{name: "a ceiling below the base still wins", base: base, max: 30 * time.Minute, n: 0, want: 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TTLFor(tt.base, tt.max, tt.n); got != tt.want {
				t.Fatalf("TTLFor(%v, %v, %d) = %v, want %v", tt.base, tt.max, tt.n, got, tt.want)
			}
		})
	}

	// With no ceiling an absurd byte count must still produce a sane positive
	// duration rather than wrapping into the past.
	if got := TTLFor(base, 0, math.MaxInt64); got < base {
		t.Fatalf("TTLFor(%v, 0, MaxInt64) = %v, want at least the base", base, got)
	}
}

// The client transfers a batch's objects one after another, so every URL in it
// has to outlive the whole batch: TTL comes from the summed size, not from the
// individual object's.
func TestBatchTTLCoversTheWholeBatch(t *testing.T) {
	const gib = int64(1) << 30
	oids := []string{
		goodOID,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	}
	objs := make([]ObjectRef, 0, len(oids))
	for _, oid := range oids {
		objs = append(objs, ObjectRef{OID: oid, Size: gib})
	}

	rec := &fakeRecorder{}
	st := newKeyStorage(nil) // nothing stored: every object needs an upload
	h := keyTestHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{Operation: "upload", Objects: objs}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	want := int(TTLFor(h.ttl, h.maxTTL, 3*gib).Seconds())
	perObject := int(TTLFor(h.ttl, h.maxTTL, gib).Seconds())
	if want <= perObject {
		t.Fatalf("test is not meaningful: batch ttl %d <= per-object ttl %d", want, perObject)
	}
	for i, obj := range resp.Objects {
		up, ok := obj.Actions["upload"]
		if !ok {
			t.Fatalf("object %d has no upload action: %+v", i, obj)
		}
		if up.ExpiresIn != want {
			t.Errorf("object %d expires_in = %d, want %d (the whole batch, not one object at %d)",
				i, up.ExpiresIn, want, perObject)
		}
	}
}

// Deduplicated objects are never transferred, so they must not stretch the
// lifetime of the URLs for the ones that are.
func TestBatchTTLIgnoresDeduplicatedObjects(t *testing.T) {
	const gib = int64(1) << 30
	dedup := strings.Repeat("c", 64)

	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{storage.LFSKey(dedup): 100 * gib})
	h := keyTestHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "upload",
		Objects: []ObjectRef{
			{OID: dedup, Size: 100 * gib}, // already stored, nothing to transfer
			{OID: goodOID, Size: gib},
		},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Actions != nil {
		t.Fatalf("object 0 = %+v, want a deduplicated hit with no actions", resp.Objects[0])
	}
	up, ok := resp.Objects[1].Actions["upload"]
	if !ok {
		t.Fatalf("object 1 has no upload action: %+v", resp.Objects[1])
	}
	if want := int(TTLFor(h.ttl, h.maxTTL, gib).Seconds()); up.ExpiresIn != want {
		t.Fatalf("expires_in = %d, want %d: the 100 GiB dedup hit must not count", up.ExpiresIn, want)
	}
}

// The bytes behind a signed PUT are unverified until verify runs, so the URL
// must never name the shared content-addressed key.
func TestBatchUploadSignsStagingKeyNotContentKey(t *testing.T) {
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.signing = true
	h := keyTestHandler(rec, st)

	resp, err := h.Batch(context.Background(), 7, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: goodOID, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if len(st.signedPuts) != 1 {
		t.Fatalf("signed PUT keys = %v, want exactly one", st.signedPuts)
	}
	staging := storage.LFSStagingKey(7, goodOID)
	if st.signedPuts[0] != staging {
		t.Errorf("signed PUT key = %q, want the staging key %q", st.signedPuts[0], staging)
	}
	if st.signedPuts[0] == storage.LFSKey(goodOID) {
		t.Error("the upload URL points at the shared content-addressed key")
	}
	if href := resp.Objects[0].Actions["upload"].Href; !strings.Contains(href, staging) {
		t.Errorf("upload href = %q, want the staging key in it", href)
	}
}

func TestVerifyPromotesStagedObjectInOrder(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{staging: 10})
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, 10); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := []string{
		"stat " + staging,
		"copy " + staging + " -> " + storage.LFSKey(goodOID),
		"record",
		"stat " + storage.LFSKey(goodOID), // confirmPresent, under the row lock
		"delete " + staging,
	}
	got := st.log.ops
	// confirmPresent runs inside RecordLFSObject, so it lands between "record"
	// and the delete; compare the sequence with that nesting spelled out.
	if len(got) != len(want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("operations = %v, want %v", got, want)
		}
	}
	if _, still := st.objects[staging]; still {
		t.Error("the staged object was left behind after a successful promotion")
	}
	if size := st.objects[storage.LFSKey(goodOID)]; size != 10 {
		t.Errorf("promoted object size = %d, want 10", size)
	}
}

// Bytes whose length contradicts the client's own declaration must stay in
// staging: promoting them would corrupt the object for every repository that
// references this oid.
func TestVerifyRefusesSizeMismatchWithoutPromoting(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{staging: 5})
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, 10)
	if err == nil {
		t.Fatal("Verify accepted a 5-byte object declared as 10 bytes")
	}
	var mismatch *SizeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Verify error = %v, want a SizeMismatchError", err)
	}
	if _, ok := st.objects[storage.LFSKey(goodOID)]; ok {
		t.Error("the mismatched object was promoted to the content-addressed key")
	}
	if _, ok := st.objects[staging]; !ok {
		t.Error("the staged object was deleted; it should be left for the collector")
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0 for a mismatch", rec.calls)
	}
	for _, op := range st.log.ops {
		if strings.HasPrefix(op, "copy ") {
			t.Errorf("operations = %v, want no copy", st.log.ops)
		}
	}
}

// git-lfs retries verify, and the same object can be verified through more
// than one path, so a second call against an already-promoted object is an
// ordinary success rather than "was not uploaded".
func TestVerifyIsIdempotent(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{staging: 10})
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, 10); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := h.Verify(context.Background(), 1, goodOID, 10); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	// The retry does not re-link: the link this repository already holds is
	// what proves the first promotion ran, so there is nothing left to record.
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want 1 (the retry re-links nothing)", rec.calls)
	}
	if size := st.objects[storage.LFSKey(goodOID)]; size != 10 {
		t.Errorf("promoted object size = %d, want 10", size)
	}
}

// A repository that never staged the object must not be able to claim it by
// verifying an oid it merely knows. lfs/ keys carry no repository, so the
// object being present says nothing about entitlement; before staging existed
// this path linked on presence alone, which turned verify into a way around
// the ownership gate every other path enforces (resolve.go's ownedLFSKey,
// commit.go, the download half of Batch).
func TestVerifyRefusesToClaimAnObjectThisRepositoryNeverStaged(t *testing.T) {
	// The bytes are published -- somebody else uploaded them -- but repo 2
	// has no staging object and no link.
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{storage.LFSKey(goodOID): 10})
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 2, goodOID, 10)
	if err == nil || !strings.Contains(err.Error(), "was not uploaded") {
		t.Fatalf("Verify error = %v, want 'was not uploaded'", err)
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0: the object must not be linked", rec.calls)
	}
	if rec.owned[fmt.Sprintf("2/%s", goodOID)] {
		t.Error("repository 2 ended up linked to an object it never uploaded")
	}
}

func TestVerifyFailsWhenNothingWasUploaded(t *testing.T) {
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, 10)
	if err == nil || !strings.Contains(err.Error(), "was not uploaded") {
		t.Fatalf("Verify error = %v, want 'was not uploaded'", err)
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0", rec.calls)
	}
}

// PromoteStaged is the emulator proxy path's entry point into the same
// sequence Verify uses.
func TestPromoteStagedPublishesAndLinks(t *testing.T) {
	staging := storage.LFSStagingKey(3, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{staging: 42})
	h := keyTestHandler(rec, st)

	if err := h.PromoteStaged(context.Background(), 3, goodOID, 42); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}
	if size := st.objects[storage.LFSKey(goodOID)]; size != 42 {
		t.Errorf("promoted object size = %d, want 42", size)
	}
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
}

func TestPromoteStagedRejectsMalformedOID(t *testing.T) {
	h := keyTestHandler(&fakeRecorder{}, newKeyStorage(nil))
	if err := h.PromoteStaged(context.Background(), 3, "../../etc/passwd", 1); err == nil {
		t.Fatal("PromoteStaged accepted an oid that is not a digest")
	}
}
