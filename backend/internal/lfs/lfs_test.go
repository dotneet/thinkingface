package lfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Promotion hashes what it publishes, so a test that stages bytes has to stage
// the *right* bytes: goodBody is the object, goodOID its digest and goodSize
// its length. They are derived rather than written out so the three can never
// drift apart.
var (
	goodBody = []byte("the bytes this oid names")
	goodOID  = oidOf(goodBody)
	goodSize = int64(len(goodBody))
)

func oidOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

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
// (0 means the object is missing from the start). content is what a read of
// any key returns, for the tests that reach the digest check.
type stubStorage struct {
	nStat      int
	presentFor int
	size       int64
	content    []byte
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
	return io.NopCloser(bytes.NewReader(s.content)), nil
}
func (s *stubStorage) GetWithGeneration(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(s.content)), 1, nil
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
		// Generation matches what GetWithGeneration reports: this stub never
		// rewrites an object, so the digest check's "did it change while I was
		// reading it" comparison must pass.
		return storage.ObjectInfo{Size: s.size, Generation: 1}, nil
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

// Upload dedup answers "you do not need to send these bytes, and the object is
// now linked to your repository", and its only evidence is the client's own
// declaration: knowing the oid *and* the size is taken as knowing the content.
// Half of that is public -- every LFS pointer in every readable repository is
// an oid -- so the size is the half that has to be checked, and declaring zero
// used to skip the check rather than fail it. That turned one batch request
// into a link to somebody else's object, which download, resolve and the
// transfer proxy all then read as entitlement.
func TestBatchUploadRefusesToDedupOnAnUndeclaredSize(t *testing.T) {
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{storage.LFSKey(goodOID): goodSize})
	h := keyTestHandler(rec, st)

	resp, err := h.Batch(context.Background(), 9, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: goodOID, Size: 0}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0: nothing here evidenced the content", rec.calls)
	}
	if rec.owned[fmt.Sprintf("9/%s", goodOID)] {
		t.Error("repository 9 was linked to an object it never uploaded")
	}
	// The client is told to upload instead, which is the honest answer: prove
	// it holds the bytes by sending them.
	if _, ok := resp.Objects[0].Actions["upload"]; !ok {
		t.Fatalf("object = %+v, want an upload action rather than a deduplicated hit", resp.Objects[0])
	}
}

// Zero is refused as a *disagreement*, never as a value: a genuinely empty LFS
// object declares zero and is zero, so its sizes agree like any other pair and
// it deduplicates normally. .gitattributes routes files to LFS by path rather
// than by content, so an empty tracked file is an ordinary thing for git-lfs
// to push and this has to keep working.
func TestBatchUploadDedupsAGenuinelyEmptyObject(t *testing.T) {
	emptyOID := oidOf(nil)
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{storage.LFSKey(emptyOID): 0})
	h := keyTestHandler(rec, st)

	resp, err := h.Batch(context.Background(), 9, &BatchRequest{
		Operation: "upload",
		Objects:   []ObjectRef{{OID: emptyOID, Size: 0}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Actions != nil || resp.Objects[0].Error != nil {
		t.Fatalf("object = %+v, want a deduplicated hit with no actions", resp.Objects[0])
	}
	if !rec.owned[fmt.Sprintf("9/%s", emptyOID)] {
		t.Error("the empty object was not linked to the repository")
	}
}

func TestVerifyFailsWhenGCDeletedDuringRecord(t *testing.T) {
	rec := &fakeRecorder{}
	// Verify stats the staging key twice -- once to size it, once after
	// hashing to confirm it did not change underneath -- so the third Stat is
	// confirmPresent's, and that is the one that must miss here.
	st := &stubStorage{presentFor: 2, size: goodSize, content: goodBody}
	h := testHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, goodSize)
	if err == nil || !strings.Contains(err.Error(), "was not uploaded") {
		t.Fatalf("Verify error = %v, want 'was not uploaded'", err)
	}
	if rec.calls != 1 {
		t.Fatalf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
}

func TestVerifySucceedsWhenObjectStillPresent(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 3, size: goodSize, content: goodBody}
	h := testHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
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
//
// objects holds sizes so a test can name a 100 GiB object without allocating
// one; bodies holds the actual bytes for the keys whose content matters, which
// is every key a promotion reads to confirm the digest.
type keyStorage struct {
	*stubStorage
	signing     bool
	objects     map[string]int64
	bodies      map[string][]byte
	generations map[string]int64
	log         *opLog
	// signedPuts records every key a PUT was signed for, in order.
	signedPuts []string
	// onRead runs when an object's bytes are read, so a test can simulate a
	// client overwriting a staged object while it is being hashed.
	onRead func(key string)
	// onStat runs after a Stat has read the object's size and generation but
	// before it returns them, so a test can simulate a write landing in the
	// window between a promotion's checks and the copy that follows them.
	onStat func(key string)
}

func newKeyStorage(objects map[string]int64) *keyStorage {
	if objects == nil {
		objects = map[string]int64{}
	}
	k := &keyStorage{
		stubStorage: &stubStorage{},
		objects:     objects,
		bodies:      map[string][]byte{},
		generations: map[string]int64{},
		log:         &opLog{},
	}
	for key := range objects {
		k.generations[key] = 1
	}
	return k
}

// put stores real bytes at key, which is what a test has to do for any object
// a promotion will hash. Writing an object always advances its generation, as
// the object store does.
func (k *keyStorage) put(key string, body []byte) {
	k.objects[key] = int64(len(body))
	k.bodies[key] = body
	k.generations[key]++
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
	info := storage.ObjectInfo{Key: key, Size: size, Generation: k.generations[key]}
	// The hook fires with the answer already captured: what it writes lands
	// after this Stat observed the object, which is the race being modelled.
	if k.onStat != nil {
		k.onStat(key)
	}
	return info, nil
}

func (k *keyStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := k.GetWithGeneration(ctx, key)
	return rc, err
}

func (k *keyStorage) GetWithGeneration(_ context.Context, key string) (io.ReadCloser, int64, error) {
	k.log.add("read " + key)
	body, ok := k.bodies[key]
	if !ok {
		if _, sized := k.objects[key]; sized {
			return nil, 0, fmt.Errorf("keyStorage: %s has a size but no bytes; use put() when the test hashes it", key)
		}
		return nil, 0, storage.ErrNotFound
	}
	generation := k.generations[key]
	if k.onRead != nil {
		k.onRead(key)
	}
	return io.NopCloser(bytes.NewReader(body)), generation, nil
}

func (k *keyStorage) Copy(_ context.Context, srcKey, dstKey string) error {
	k.log.add("copy " + srcKey + " -> " + dstKey)
	size, ok := k.objects[srcKey]
	if !ok {
		return storage.ErrNotFound
	}
	k.objects[dstKey] = size
	if body, ok := k.bodies[srcKey]; ok {
		k.bodies[dstKey] = body
	}
	k.generations[dstKey]++
	return nil
}

func (k *keyStorage) Delete(_ context.Context, key string) error {
	k.log.add("delete " + key)
	delete(k.objects, key)
	delete(k.bodies, key)
	return nil
}

var _ storage.Storage = (*keyStorage)(nil)

func keyTestHandler(rec *fakeRecorder, st *keyStorage) *Handler {
	rec.log = st.log
	h := testHandler(rec, st)
	return h
}

func TestMaxSignedURLTTL(t *testing.T) {
	for _, tt := range []struct {
		name string
		max  time.Duration
		want time.Duration
	}{
		{"a ceiling below the signing limit is the answer", 12 * time.Hour, 12 * time.Hour},
		{"no ceiling falls back to what GCS will sign", 0, signingLimit},
		{"a negative ceiling is no ceiling", -time.Hour, signingLimit},
		{"a ceiling above the signing limit is unreachable", 30 * 24 * time.Hour, signingLimit},
		{"exactly the signing limit", signingLimit, signingLimit},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxSignedURLTTL(tt.max)
			if got != tt.want {
				t.Fatalf("MaxSignedURLTTL(%v) = %v, want %v", tt.max, got, tt.want)
			}
			// The contract callers rely on: no transfer, however large, can
			// produce a URL that outlives this.
			if ttl := TTLFor(time.Hour, tt.max, math.MaxInt64); ttl > got {
				t.Fatalf("TTLFor produced %v, above the %v this reports as the maximum", ttl, got)
			}
		})
	}
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
	st := newKeyStorage(nil)
	st.put(staging, goodBody)
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := []string{
		"stat " + staging,
		"read " + staging, // hashed before anything is published
		"stat " + staging, // and re-checked for a rewrite during that read
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
	if size := st.objects[storage.LFSKey(goodOID)]; size != goodSize {
		t.Errorf("promoted object size = %d, want %d", size, goodSize)
	}
}

// Bytes whose length contradicts the client's own declaration must stay in
// staging: promoting them would corrupt the object for every repository that
// references this oid.
func TestVerifyRefusesSizeMismatchWithoutPromoting(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, goodBody[:5])
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, goodSize)
	if err == nil {
		t.Fatalf("Verify accepted a 5-byte object declared as %d bytes", goodSize)
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
		// The size check is the cheap first cut: a truncated 10 GiB upload
		// must be rejected on metadata alone, without reading it back.
		if strings.HasPrefix(op, "read ") {
			t.Errorf("operations = %v, want no read for a size that already disagrees", st.log.ops)
		}
	}
}

// git-lfs retries verify, and the same object can be verified through more
// than one path, so a second call against an already-promoted object is an
// ordinary success rather than "was not uploaded".
func TestVerifyIsIdempotent(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, goodBody)
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	// The retry does not re-link: the link this repository already holds is
	// what proves the first promotion ran, so there is nothing left to record.
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want 1 (the retry re-links nothing)", rec.calls)
	}
	if size := st.objects[storage.LFSKey(goodOID)]; size != goodSize {
		t.Errorf("promoted object size = %d, want %d", size, goodSize)
	}
	// The retry must not re-read the promoted object either: nothing can have
	// rewritten a content-addressed key, and a 10 GiB re-hash per retry would
	// be a large bill for no additional proof.
	if reads := countOps(st.log.ops, "read "); reads != 1 {
		t.Errorf("reads = %d, want 1: only the first verify hashes", reads)
	}
}

// A verify that finds nothing in staging is answering about an object this
// repository is already linked to, so it publishes nothing and can afford to
// be lenient about a size the client left out. The proxy upload path promotes
// before git-lfs ever calls verify, so this is the ordinary shape of a verify
// there, not an edge case.
func TestVerifyRetryToleratesAnUnstatedSize(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, goodBody)
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := h.Verify(context.Background(), 1, goodOID, 0); err != nil {
		t.Fatalf("retry with an unstated size: %v", err)
	}
	// The same leniency must not exist where bytes are published: a staged
	// object declared as zero is refused (see
	// TestVerifyRefusesForgedBytesDeclaredAsZeroSize).
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
}

func countOps(ops []string, prefix string) int {
	n := 0
	for _, op := range ops {
		if strings.HasPrefix(op, prefix) {
			n++
		}
	}
	return n
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
// sequence Verify uses. That path hashed the body as it received it, so
// promotion takes its word for the digest and never reads the object back --
// the fake has no bytes for this key at all, which is the assertion.
func TestPromoteStagedFromPublishesAndLinksWithoutRehashing(t *testing.T) {
	staging := storage.LFSIncomingKey(3, "0123456789abcdef")
	rec := &fakeRecorder{}
	st := newKeyStorage(map[string]int64{staging: 42})
	h := keyTestHandler(rec, st)

	if err := h.PromoteStagedFrom(context.Background(), 3, goodOID, 42, staging); err != nil {
		t.Fatalf("PromoteStagedFrom: %v", err)
	}
	if size := st.objects[storage.LFSKey(goodOID)]; size != 42 {
		t.Errorf("promoted object size = %d, want 42", size)
	}
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
	if reads := countOps(st.log.ops, "read "); reads != 0 {
		t.Errorf("reads = %d, want 0: the ingest hash is the proof on this path", reads)
	}
}

// The window between a promotion's checks and its copy is the one place where
// "the bytes I inspected" and "the bytes at this key" can come apart, and what
// it publishes onto is lfs/{oid}: a key every repository on the instance
// shares, that dedup treats as authoritative, and that nothing rewrites
// afterwards. storage.LFSStagingKey is named after the repository and the oid
// -- both of them the client's own words -- and its upload URL can still be
// live, so a second writer can land on it; the check therefore has to run for
// a caller that hashed the bytes on ingest too, because that promise is about
// the body *that* request received and says nothing about what is at the key
// when the copy runs.
func TestPromoteStagedFromRefusesStagingRewrittenBeforeTheCopy(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	published := storage.LFSKey(goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(published, goodBody) // other repositories already reference these bytes
	st.put(staging, goodBody)
	// Same length as the real object, so the size check cannot tell them
	// apart: only the generation can.
	forged := bytes.Repeat([]byte("x"), int(goodSize))
	st.onStat = func(key string) {
		if key == staging {
			st.onStat = nil // a single racing write, landing after the size check
			st.put(staging, forged)
		}
	}
	h := keyTestHandler(rec, st)

	err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, staging)
	var changed *StagedObjectChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("PromoteStagedFrom error = %v, want a StagedObjectChangedError", err)
	}
	if got := st.bodies[published]; !bytes.Equal(got, goodBody) {
		t.Fatalf("%s now holds %q: the shared content-addressed key was overwritten", published, got)
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0", rec.calls)
	}
	for _, op := range st.log.ops {
		if strings.HasPrefix(op, "copy ") {
			t.Errorf("operations = %v, want no copy", st.log.ops)
		}
	}
	if _, ok := st.objects[staging]; !ok {
		t.Error("the staged object was deleted; it should be left for the collector")
	}

	// ...and the client's own re-upload promotes normally once the writes
	// have settled, so the check costs a retry rather than the object.
	st.put(staging, goodBody)
	if err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, staging); err != nil {
		t.Fatalf("PromoteStagedFrom after the race settled: %v", err)
	}
	if size := st.objects[published]; size != goodSize {
		t.Errorf("promoted object size = %d, want %d", size, goodSize)
	}
}

func TestPromoteStagedFromRejectsMalformedOID(t *testing.T) {
	h := keyTestHandler(&fakeRecorder{}, newKeyStorage(nil))
	staging := storage.LFSIncomingKey(3, "0123456789abcdef")
	if err := h.PromoteStagedFrom(context.Background(), 3, "../../etc/passwd", 1, staging); err == nil {
		t.Fatal("PromoteStagedFrom accepted an oid that is not a digest")
	}
	if err := h.PromoteStagedFrom(context.Background(), 3, goodOID, 1, ""); err == nil {
		t.Fatal("PromoteStagedFrom accepted an empty staging key")
	}
}

// assertNotPublished is the assertion every refused verify shares: the bytes
// stayed in staging and lfs/{oid} was left alone. That key is shared by every
// repository on the instance and Batch treats its presence as proof the
// content exists, so anything that lands there wrongly is served to everybody.
func assertNotPublished(t *testing.T, rec *fakeRecorder, st *keyStorage, repoID int64, oid, staging string) {
	t.Helper()
	if _, ok := st.objects[storage.LFSKey(oid)]; ok {
		t.Errorf("bytes were promoted to %s", storage.LFSKey(oid))
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0", rec.calls)
	}
	if rec.owned[fmt.Sprintf("%d/%s", repoID, oid)] {
		t.Errorf("repository %d was linked to %s", repoID, oid)
	}
	if _, ok := st.objects[staging]; !ok {
		t.Error("the staged object was deleted; it should be left for the collector")
	}
}

// The signed-URL upload path is the one place where nothing on the server ever
// sees the bytes, so verify hashing them is the only thing that keeps
// lfs/{oid} content-addressed. Without it, write access to any one repository
// is enough to publish arbitrary bytes under an oid the instance does not have
// yet -- and every later push of the real object is deduplicated onto the
// forgery, in every repository.
func TestVerifyRefusesStagedBytesThatDoNotHashToTheOID(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	forged := []byte("not the bytes this oid names!")
	if int64(len(forged)) == goodSize {
		t.Fatal("test is not meaningful: the forgery must be caught by the digest, not the size")
	}
	st.put(staging, forged)
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, int64(len(forged)))
	if err == nil {
		t.Fatal("Verify accepted bytes that do not hash to the oid they were uploaded under")
	}
	var mismatch *DigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Verify error = %v, want a DigestMismatchError", err)
	}
	if mismatch.Got != oidOf(forged) {
		t.Errorf("reported digest = %s, want the one the bytes actually have (%s)", mismatch.Got, oidOf(forged))
	}
	assertNotPublished(t, rec, st, 1, goodOID, staging)
}

// The declared size used to be checked only when it was positive, so a verify
// declaring zero turned the check off entirely: upload anything, verify with
// size 0, and it was promoted. Both halves have to hold now -- a size that
// disagrees with the object is refused whatever its value, and the digest is
// checked regardless of what the size said.
func TestVerifyRefusesForgedBytesDeclaredAsZeroSize(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, []byte("not the bytes this oid names!"))
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, 0); err == nil {
		t.Fatal("Verify accepted a non-empty object declared as 0 bytes")
	}
	assertNotPublished(t, rec, st, 1, goodOID, staging)
}

// The same declaration with an object whose size really is zero: the size
// agrees, so only the digest can catch it. A genuinely empty object is legal,
// which is why zero is checked rather than rejected outright.
func TestVerifyRefusesEmptyStagedObjectUnderAnotherOID(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, []byte{})
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, 0)
	var mismatch *DigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Verify error = %v, want a DigestMismatchError", err)
	}
	assertNotPublished(t, rec, st, 1, goodOID, staging)

	// ...and the empty object under its own oid is fine.
	emptyOID := oidOf(nil)
	emptyStaging := storage.LFSStagingKey(1, emptyOID)
	st.put(emptyStaging, []byte{})
	if err := h.Verify(context.Background(), 1, emptyOID, 0); err != nil {
		t.Fatalf("Verify of a genuinely empty object: %v", err)
	}
}

func TestVerifyRejectsNegativeSize(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, goodBody)
	h := keyTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, -1); err == nil {
		t.Fatal("Verify accepted a negative size")
	}
	assertNotPublished(t, rec, st, 1, goodOID, staging)
}

// The upload URL for a staging key can still be live while verify runs, so
// bytes that hashed correctly may be replaced before the copy. Generations
// move forward on every write, so a staging object that moved while it was
// being read is refused rather than promoted on the strength of a digest that
// no longer describes it.
func TestVerifyRefusesStagedObjectRewrittenWhileHashing(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newKeyStorage(nil)
	st.put(staging, goodBody)
	st.onRead = func(key string) {
		if key == staging {
			st.onRead = nil // only the first read races
			st.put(staging, goodBody)
		}
	}
	h := keyTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, goodSize)
	var changed *StagedObjectChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("Verify error = %v, want a StagedObjectChangedError", err)
	}
	assertNotPublished(t, rec, st, 1, goodOID, staging)

	// git-lfs retries verify, and the retry hashes whatever is in staging
	// now, so a benign concurrent re-upload still ends in a published object.
	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
		t.Fatalf("Verify after the rewrite settled: %v", err)
	}
}
