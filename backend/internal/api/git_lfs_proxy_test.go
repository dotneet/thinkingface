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
