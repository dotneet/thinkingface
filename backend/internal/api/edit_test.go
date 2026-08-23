package api

import (
	"strings"
	"testing"
)

// ------------------------------------------------------------ commitSummary

func TestCommitSummary(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		message     string
		description string
		want        string
	}{
		{"message and description", "README.md", "Fix typo", "Also reword intro", "Fix typo\n\nAlso reword intro"},
		{"message only", "README.md", "Fix typo", "", "Fix typo"},
		{"default message", "docs/notes.txt", "", "", "Update docs/notes.txt"},
		{"default message with description", "docs/notes.txt", "", "add context", "Update docs/notes.txt\n\nadd context"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitSummary(tt.path, tt.message, tt.description); got != tt.want {
				t.Errorf("commitSummary(%q, %q, %q) = %q, want %q", tt.path, tt.message, tt.description, got, tt.want)
			}
		})
	}
}

// ------------------------------------------------------------- editConflict

func TestEditConflict(t *testing.T) {
	tests := []struct {
		name          string
		baseOID       string
		exists        bool
		currentOID    string
		wantConflict  bool
		wantMsgSubstr string
	}{
		{"no base_oid never conflicts", "", false, "", false, ""},
		{"no base_oid ignores existing content", "", true, "abc123", false, ""},
		{"matching base_oid on existing file", "abc123", true, "abc123", false, ""},
		{"stale base_oid on existing file", "abc123", true, "def456", true, "current blob is def456"},
		{"base_oid but file now missing", "abc123", false, "", true, "no longer exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, isConflict := editConflict(tt.baseOID, tt.exists, tt.currentOID)
			if isConflict != tt.wantConflict {
				t.Fatalf("editConflict(%q, %v, %q) isConflict = %v, want %v", tt.baseOID, tt.exists, tt.currentOID, isConflict, tt.wantConflict)
			}
			if tt.wantConflict && !strings.Contains(msg, tt.wantMsgSubstr) {
				t.Errorf("editConflict(%q, %v, %q) message = %q, want substring %q", tt.baseOID, tt.exists, tt.currentOID, msg, tt.wantMsgSubstr)
			}
			if !tt.wantConflict && msg != "" {
				t.Errorf("editConflict(%q, %v, %q) message = %q, want empty", tt.baseOID, tt.exists, tt.currentOID, msg)
			}
		})
	}
}

// --------------------------------------------------------- lfsEditRejection

func TestLFSEditRejection(t *testing.T) {
	msg := lfsEditRejection("model.safetensors")
	if !strings.Contains(msg, "model.safetensors") {
		t.Errorf("lfsEditRejection message = %q, want it to mention the path", msg)
	}
	if !strings.Contains(msg, "LFS") {
		t.Errorf("lfsEditRejection message = %q, want it to mention LFS", msg)
	}
}
