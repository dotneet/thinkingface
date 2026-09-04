package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/lfs"
)

// A failed verify is not always "not there": the status has to say whether
// the pusher should re-upload, fix its bytes, retry, or ask the operator for
// more quota.
func TestWriteLFSVerifyErrorStatusCodes(t *testing.T) {
	const oid = "1111111111111111111111111111111111111111111111111111111111111111"
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"quota", &lfs.QuotaExceededError{Namespace: "acme", Message: "over quota"},
			http.StatusInsufficientStorage},
		{"size mismatch", &lfs.SizeMismatchError{OID: oid, Got: 5, Want: 10},
			http.StatusUnprocessableEntity},
		{"digest mismatch", &lfs.DigestMismatchError{OID: oid, Got: oid},
			http.StatusUnprocessableEntity},
		{"staged object changed", &lfs.StagedObjectChangedError{OID: oid},
			http.StatusConflict},
		{"never uploaded", errors.New("object " + oid + " was not uploaded"),
			http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeLFSVerifyError(rec, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			// The LFS media type is the protocol's to dictate, on every
			// answer including the errors.
			if ct := rec.Header().Get("Content-Type"); ct != lfs.ContentType {
				t.Fatalf("Content-Type = %q, want %q", ct, lfs.ContentType)
			}
		})
	}
}
