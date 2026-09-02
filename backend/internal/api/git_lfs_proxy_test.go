package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// The emulator's transfer proxy is the entire upload path wherever signed URLs
// are unavailable -- the local stack, and the only path the E2E suite can
// reach -- so its happy path is pinned here rather than left to E2E alone:
// bytes in, object published under its content address, repository linked,
// nothing left in staging.
func TestLFSProxyUpload_PublishesUnderTheContentAddressAndLinksTheRepo(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("the bytes of a very small model")
	oid := oidOfBytes(body)

	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oid,
		rawBody: body,
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}

	if got := readObject(t, f, storage.LFSKey(oid)); !bytes.Equal(got, body) {
		t.Errorf("published bytes = %q, want %q", got, body)
	}
	owned, err := f.st.RepoHasLFSObject(context.Background(), repo.ID, oid)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !owned {
		t.Error("the repository was not linked to the object it just uploaded")
	}
	assertStagingIsClean(t, f)
}

// The proxy is the one upload path that sees the bytes, so it settles the
// digest itself and never stages anything under the shared key when it
// disagrees.
func TestLFSProxyUpload_RefusesBytesThatDoNotHashToTheDeclaredOID(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	declared := oidOfBytes([]byte("the bytes this oid names"))
	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + declared,
		rawBody: []byte("something else entirely"),
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}
	if _, err := f.obj.Stat(context.Background(), storage.LFSKey(declared)); err == nil {
		t.Errorf("bytes reached %s, the key every repository on the instance shares", storage.LFSKey(declared))
	}
	owned, err := f.st.RepoHasLFSObject(context.Background(), repo.ID, declared)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if owned {
		t.Error("the repository was linked to an oid it never successfully uploaded")
	}
	assertStagingIsClean(t, f)
}

// A refused or completed proxy upload must not leave its staged bytes behind.
// The keys are private to one request now (storage.LFSIncomingKey), so nothing
// else will ever name them: what is not deleted here waits for `thinkingface
// gc` instead.
func assertStagingIsClean(t *testing.T, f *secFixture) {
	t.Helper()
	left, err := f.obj.List(context.Background(), storage.LFSStagingPrefix)
	if err != nil {
		t.Fatalf("list staging: %v", err)
	}
	if len(left) != 0 {
		keys := make([]string, 0, len(left))
		for _, o := range left {
			keys = append(keys, o.Key)
		}
		t.Errorf("staged objects left behind: %s", strings.Join(keys, ", "))
	}
}

func readObject(t *testing.T, f *secFixture, key string) []byte {
	t.Helper()
	rc, err := f.obj.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return data
}

func oidOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// signingStorage is the emulator's store claiming it can sign URLs, which is
// what a real GCS deployment looks like from the handler's side.
type signingStorage struct{ storage.Storage }

func (signingStorage) SupportsSignedURL() bool { return true }

// In signed-URL mode nothing hands a client this href -- uploadAction mints a
// proxy URL only when the driver cannot sign one -- so the route existed as an
// unbounded, unquota'd write into the bucket for any holder of a write-scoped
// token, reachable without a batch response at all. It now refuses.
func TestLFSProxyUpload_RefusedWhenTheDriverCanSignURLs(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")
	f.s.storage = signingStorage{f.obj}

	body := []byte("the bytes of a very small model")
	oid := oidOfBytes(body)
	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oid,
		rawBody: body,
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
	}
	if _, err := f.obj.Stat(context.Background(), storage.LFSKey(oid)); err == nil {
		t.Error("bytes reached the bucket through a route no client is ever given")
	}
}

// The download half is what the emulator's `git lfs pull` uses, so the gate
// above must not become a gate on both modes: in the mode the local stack and
// the E2E suite actually run in, this href is the only way the bytes come out.
func TestLFSProxyDownload_ServesTheObjectInEmulatorMode(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("weights alice pushed")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	rec := f.do(secRequest{
		method: "GET",
		path:   "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oid,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), body)
	}
}

// The mirror of TestLFSProxyUpload_RefusedWhenTheDriverCanSignURLs, and the
// half with the wider mouth: the fallback authorisation on this route asks
// only that the repository link the oid, and both the repository id and the
// oid are public, so while it answered in signed-URL mode an *anonymous*
// caller could stream whole objects through the API process -- turning the
// signed-URL offload the deployment exists for back into egress and CPU here.
func TestLFSProxyDownload_RefusedWhenTheDriverCanSignURLs(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("weights alice pushed")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	f.s.storage = signingStorage{f.obj}

	rec := f.do(secRequest{
		method: "GET",
		path:   "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oid,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), body) {
		t.Errorf("the object streamed through a route no client is ever given: %s", rec.Body.String())
	}
}

// The body is a raw object with no declared length the server has agreed to,
// so the only ceiling available is an explicit one. Without it this handler
// streamed an unbounded body into the bucket -- and streamingRoute exempts the
// path from handlerTimeout, so there was no other limit either.
func TestLFSProxyUpload_RefusesABodyPastTheCeiling(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")
	tok := f.token(alice, "write")

	// Lowered rather than exercised at its real value: proving a 10 GiB
	// ceiling by sending 10 GiB costs more than the ceiling is worth.
	restore := maxLFSProxyObjectBytes
	maxLFSProxyObjectBytes = 16
	t.Cleanup(func() { maxLFSProxyObjectBytes = restore })

	oversized := bytes.Repeat([]byte("x"), 17)
	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oidOfBytes(oversized),
		rawBody: oversized,
		headers: map[string]string{"Authorization": "Bearer " + tok},
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s; want 413", rec.Code, rec.Body.String())
	}
	assertStagingIsClean(t, f)

	// The cap is inclusive: exactly the ceiling is a legitimate object.
	exact := bytes.Repeat([]byte("x"), 16)
	rec = f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oidOfBytes(exact),
		rawBody: exact,
		headers: map[string]string{"Authorization": "Bearer " + tok},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status for a body of exactly the ceiling = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
}

// The transfer proxy publishes and links through lfs.PromoteStagedFrom without
// passing through the LFS batch API, so while the quota lived only in Batch a
// caller could PUT here with a write token and grow a full namespace without
// limit.
func TestLFSProxyUpload_RefusesWhenTheNamespaceHasNoRoom(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")
	zero := int64(0)
	if err := f.st.SetNamespaceQuota(context.Background(), "alice", &zero); err != nil {
		t.Fatalf("set namespace quota: %v", err)
	}

	body := []byte("the bytes of a very small model")
	oid := oidOfBytes(body)
	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/lfs/" + strconv.FormatInt(repo.ID, 10) + "/" + oid,
		rawBody: body,
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, body = %s; want 507", rec.Code, rec.Body.String())
	}
	if _, err := f.obj.Stat(context.Background(), storage.LFSKey(oid)); err == nil {
		t.Error("the object was published despite the namespace being full")
	}
	owned, err := f.st.RepoHasLFSObject(context.Background(), repo.ID, oid)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if owned {
		t.Error("a refused upload was linked to the repository")
	}
	assertStagingIsClean(t, f)
}
