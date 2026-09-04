package lfs

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A batch naming more objects than the server decides in one request is
// refused as a whole, before a single storage round trip. The body ceiling
// alone is not a bound on the work: 8 MiB of minimal records is hundreds of
// thousands of them, each costing at least a Stat on the upload path.
func TestBatchRefusesMoreObjectsThanTheLimit(t *testing.T) {
	h := testHandler(&fakeRecorder{}, &stubStorage{})

	objs := make([]ObjectRef, MaxBatchObjects+1)
	for i := range objs {
		objs[i] = ObjectRef{OID: oidOf([]byte(fmt.Sprintf("object %d", i))), Size: 1}
	}
	if _, err := h.Batch(context.Background(), 1,
		&BatchRequest{Operation: "upload", Objects: objs}, ""); !errors.Is(err, ErrTooManyObjects) {
		t.Fatalf("Batch with %d objects: err = %v, want ErrTooManyObjects", len(objs), err)
	}
}

// The limit is a bound, not a target: a batch of exactly MaxBatchObjects is
// still decided normally.
func TestBatchAcceptsExactlyTheLimit(t *testing.T) {
	h := testHandler(&fakeRecorder{}, &stubStorage{})

	objs := make([]ObjectRef, MaxBatchObjects)
	for i := range objs {
		objs[i] = ObjectRef{OID: oidOf([]byte(fmt.Sprintf("object %d", i))), Size: 1}
	}
	resp, err := h.Batch(context.Background(), 1,
		&BatchRequest{Operation: "upload", Objects: objs}, "")
	if err != nil {
		t.Fatalf("Batch with %d objects: %v", len(objs), err)
	}
	if len(resp.Objects) != len(objs) {
		t.Fatalf("Batch returned %d objects, want %d", len(resp.Objects), len(objs))
	}
}

// The count applies to downloads too: deciding ten thousand memberships is
// the same work whatever the operation.
func TestBatchRefusesMoreDownloadsThanTheLimit(t *testing.T) {
	h := testHandler(&fakeRecorder{}, &stubStorage{})

	objs := make([]ObjectRef, MaxBatchObjects+1)
	for i := range objs {
		objs[i] = ObjectRef{OID: oidOf([]byte(fmt.Sprintf("object %d", i))), Size: 1}
	}
	if _, err := h.Batch(context.Background(), 1,
		&BatchRequest{Operation: "download", Objects: objs}, ""); !errors.Is(err, ErrTooManyObjects) {
		t.Fatalf("download Batch with %d objects: err = %v, want ErrTooManyObjects", len(objs), err)
	}
}
