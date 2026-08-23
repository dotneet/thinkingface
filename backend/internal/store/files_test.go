package store

import (
	"context"
	"testing"
)

func TestRecordLFSObject_RequiresConfirmPresent(t *testing.T) {
	s := &Store{}
	err := s.RecordLFSObject(context.Background(), 1, "oid", 1, nil)
	if err == nil {
		t.Fatal("RecordLFSObject(nil confirmPresent): want an error")
	}
}
