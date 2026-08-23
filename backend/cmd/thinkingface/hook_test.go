package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/wal"
)

func TestParsePreReceiveInput_SingleRef(t *testing.T) {
	input := "0000000000000000000000000000000000000000 3f7a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2 refs/heads/main\n"
	got, err := parsePreReceiveInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parsePreReceiveInput: %v", err)
	}
	want := []wal.RefUpdate{
		{
			Ref: "refs/heads/main",
			Old: "0000000000000000000000000000000000000000",
			New: "3f7a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2",
		},
	}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("parsePreReceiveInput() = %+v, want %+v", got, want)
	}
}

func TestParsePreReceiveInput_MultipleRefs(t *testing.T) {
	input := strings.Join([]string{
		"aaaa1111 bbbb2222 refs/heads/main",
		"0000000000000000000000000000000000000000 cccc3333 refs/heads/feature",
		"dddd4444 0000000000000000000000000000000000000000 refs/tags/v1.0",
		"",
	}, "\n")
	got, err := parsePreReceiveInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parsePreReceiveInput: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if got[1].Ref != "refs/heads/feature" || got[1].Old != "0000000000000000000000000000000000000000" {
		t.Fatalf("got[1] = %+v", got[1])
	}
	if got[2].New != "0000000000000000000000000000000000000000" {
		t.Fatalf("got[2] = %+v (expected a delete: new == zero-oid)", got[2])
	}
}

func TestParsePreReceiveInput_MalformedLine(t *testing.T) {
	_, err := parsePreReceiveInput(strings.NewReader("only-two-fields refs/heads/main\n"))
	if err == nil {
		t.Fatal("parsePreReceiveInput() error = nil, want error for malformed line")
	}
}

func TestParsePreReceiveInput_Empty(t *testing.T) {
	got, err := parsePreReceiveInput(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parsePreReceiveInput: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestRunPreReceiveHook_ModeOffPassesThrough(t *testing.T) {
	t.Setenv("TF_WAL_MODE", "off")
	var stderr bytes.Buffer
	if err := runPreReceiveHook(strings.NewReader("aaaa1111 bbbb2222 refs/heads/main\n"), &stderr); err != nil {
		t.Fatalf("mode=off must accept the push, got %v", err)
	}
}

func TestRunPreReceiveHook_ModeUnsetPassesThrough(t *testing.T) {
	t.Setenv("TF_WAL_MODE", "")
	var stderr bytes.Buffer
	if err := runPreReceiveHook(strings.NewReader("aaaa1111 bbbb2222 refs/heads/main\n"), &stderr); err != nil {
		t.Fatalf("mode unset must accept the push, got %v", err)
	}
}

func TestRunPreReceiveHook_UnknownModeFailsClosed(t *testing.T) {
	t.Setenv("TF_WAL_MODE", "authoratitive") // typo on purpose
	var stderr bytes.Buffer
	if err := runPreReceiveHook(strings.NewReader("aaaa1111 bbbb2222 refs/heads/main\n"), &stderr); err == nil {
		t.Fatal("a typoed mode must reject the push, not silently bypass the WAL")
	}
	if !strings.Contains(stderr.String(), "unknown TF_WAL_MODE") {
		t.Fatalf("stderr = %q, want the unknown-mode message", stderr.String())
	}
}

func TestRunPreReceiveHook_ShadowSwallowsSetupFailure(t *testing.T) {
	// Shadow mode with the WAL env missing: the write cannot even start, but
	// the push must still be accepted — disk is authoritative in this phase.
	t.Setenv("TF_WAL_MODE", "shadow")
	t.Setenv("TF_WAL_STORAGE_PATH", "")
	var stderr bytes.Buffer
	if err := runPreReceiveHook(strings.NewReader("aaaa1111 bbbb2222 refs/heads/main\n"), &stderr); err != nil {
		t.Fatalf("shadow mode must never fail the push, got %v", err)
	}
	if !strings.Contains(stderr.String(), "WAL shadow write failed") {
		t.Fatalf("stderr = %q, want the shadow-failure warning", stderr.String())
	}
}

func TestRunPreReceiveHook_AuthoritativeSetupFailureRejects(t *testing.T) {
	t.Setenv("TF_WAL_MODE", "authoritative")
	t.Setenv("TF_WAL_STORAGE_PATH", "")
	var stderr bytes.Buffer
	if err := runPreReceiveHook(strings.NewReader("aaaa1111 bbbb2222 refs/heads/main\n"), &stderr); err == nil {
		t.Fatal("authoritative mode with no WAL context must reject the push")
	}
}

func TestRunPreReceiveHook_EmptyInputIsANoop(t *testing.T) {
	t.Setenv("TF_WAL_MODE", "authoritative") // even the strict mode: nothing to record
	var stderr bytes.Buffer
	if err := runPreReceiveHook(strings.NewReader(""), &stderr); err != nil {
		t.Fatalf("no ref updates must be accepted, got %v", err)
	}
}

func TestRunPreReceiveHook_MalformedInputAlsoRejected(t *testing.T) {
	stdin := strings.NewReader("garbage\n")
	var stderr bytes.Buffer
	err := runPreReceiveHook(stdin, &stderr)
	if err == nil {
		t.Fatal("runPreReceiveHook() error = nil, want non-nil for malformed input")
	}
}
