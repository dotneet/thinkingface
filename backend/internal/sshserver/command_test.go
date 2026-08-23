package sshserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitserver"
)

func TestParseCommandAccepted(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want Request
	}{
		"upload-pack single quoted": {
			"git-upload-pack 'alice/mymodel'",
			Request{gitserver.UploadPack, "model", "alice", "mymodel"},
		},
		"receive-pack with .git": {
			"git-receive-pack 'alice/mymodel.git'",
			Request{gitserver.ReceivePack, "model", "alice", "mymodel"},
		},
		"leading slash (ssh:// URL form)": {
			"git-upload-pack '/alice/mymodel.git'",
			Request{gitserver.UploadPack, "model", "alice", "mymodel"},
		},
		"datasets prefix": {
			"git-upload-pack 'datasets/alice/corpus'",
			Request{gitserver.UploadPack, "dataset", "alice", "corpus"},
		},
		"models prefix": {
			"git-receive-pack '/models/alice/mymodel.git'",
			Request{gitserver.ReceivePack, "model", "alice", "mymodel"},
		},
		"double quoted": {
			`git-upload-pack "alice/mymodel"`,
			Request{gitserver.UploadPack, "model", "alice", "mymodel"},
		},
		"unquoted": {
			"git-upload-pack alice/mymodel",
			Request{gitserver.UploadPack, "model", "alice", "mymodel"},
		},
		"sub-command spelling": {
			"git upload-pack 'alice/mymodel'",
			Request{gitserver.UploadPack, "model", "alice", "mymodel"},
		},
		"dots and dashes in names": {
			"git-upload-pack 'my-org/llama-3.1-8b_v2'",
			Request{gitserver.UploadPack, "model", "my-org", "llama-3.1-8b_v2"},
		},
		"surrounding whitespace": {
			"  git-upload-pack 'alice/mymodel'  ",
			Request{gitserver.UploadPack, "model", "alice", "mymodel"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCommand(tc.raw)
			if err != nil {
				t.Fatalf("ParseCommand(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseCommand(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseCommandRejected(t *testing.T) {
	cases := map[string]string{
		// Nothing but the two git services may run.
		"empty":            "",
		"blank":            "   ",
		"shell":            "bash",
		"rm":               "rm -rf /",
		"scp":              "scp -f /etc/passwd",
		"upload-archive":   "git-upload-archive 'alice/mymodel'",
		"prefix collision": "git-upload-packet 'alice/mymodel'",
		"no argument":      "git-upload-pack",
		"empty argument":   "git-upload-pack ''",

		// Path traversal, in every spelling the parser could plausibly let
		// through. Every one of these must fail closed.
		"traversal up":            "git-upload-pack '../../etc/passwd'",
		"traversal middle":        "git-upload-pack 'alice/../../etc'",
		"traversal dotdot ns":     "git-upload-pack '../alice/mymodel'",
		"traversal dot segment":   "git-upload-pack 'alice/./mymodel'",
		"traversal encoded":       "git-upload-pack 'alice/%2e%2e/secret'",
		"traversal trailing dots": "git-upload-pack 'datasets/../../root/.ssh'",

		// Anything that could add an argument or reach a shell.
		"second argument":  "git-upload-pack 'alice/mymodel' --upload-pack=/bin/sh",
		"semicolon":        "git-upload-pack 'alice/mymodel; rm -rf /'",
		"backtick":         "git-upload-pack 'alice/`id`'",
		"dollar":           "git-upload-pack 'alice/$(id)'",
		"pipe":             "git-upload-pack 'alice|id'",
		"newline":          "git-upload-pack 'alice/mymodel\nrm -rf /'",
		"null byte":        "git-upload-pack 'alice/my\x00model'",
		"unbalanced quote": "git-upload-pack 'alice/mymodel",

		// Wrong shape, not dangerous but still not a repository.
		"one segment":         "git-upload-pack 'mymodel'",
		"four segments":       "git-upload-pack 'a/b/c/d'",
		"unknown kind prefix": "git-upload-pack 'spaces/alice/mymodel'",
		"empty namespace":     "git-upload-pack '/alice'",
		"leading dot name":    "git-upload-pack 'alice/.hidden'",
		"leading dash ns":     "git-upload-pack '-alice/mymodel'",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCommand(raw)
			if err == nil {
				t.Fatalf("ParseCommand(%q) accepted -> %+v", raw, got)
			}
			if !errors.Is(err, ErrBadCommand) {
				t.Errorf("error %v does not wrap ErrBadCommand", err)
			}
		})
	}
}

// TestParseCommandAbsolutePathIsJustANameNotAFilesystemPath documents why an
// absolute-looking path is not a rejection: it is read as ns/name like any
// other, and the repository lookup that follows finds nothing. Nothing here
// ever touches the filesystem -- the git service runs against the storage
// path stored on the repository row.
func TestParseCommandAbsolutePathIsJustANameNotAFilesystemPath(t *testing.T) {
	got, err := ParseCommand("git-upload-pack '/etc/passwd'")
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if got.Namespace != "etc" || got.Name != "passwd" {
		t.Fatalf("ParseCommand = %+v, want the segments read as a repository name", got)
	}
}

// TestParseCommandNeverYieldsAShellMetacharacter is the invariant the
// repository lookup depends on: whatever the client sends, the namespace and
// name that come out are plain identifiers.
func TestParseCommandNeverYieldsAShellMetacharacter(t *testing.T) {
	const dangerous = "/\\ \t\n\r\x00'\"`$;|&<>*?()[]{}!#~"
	inputs := []string{
		"git-upload-pack 'alice/mymodel'",
		"git-receive-pack 'datasets/my-org/corpus_v2.1.git'",
		"git upload-pack /models/a/b",
	}
	for _, raw := range inputs {
		req, err := ParseCommand(raw)
		if err != nil {
			t.Fatalf("ParseCommand(%q): %v", raw, err)
		}
		for _, field := range []string{req.Namespace, req.Name, req.Kind} {
			if strings.ContainsAny(field, dangerous) {
				t.Errorf("ParseCommand(%q) produced %q, which contains a metacharacter", raw, field)
			}
			if field == "" {
				t.Errorf("ParseCommand(%q) produced an empty field", raw)
			}
		}
		if req.Kind != "model" && req.Kind != "dataset" {
			t.Errorf("Kind = %q, want model or dataset", req.Kind)
		}
		if req.Service != gitserver.UploadPack && req.Service != gitserver.ReceivePack {
			t.Errorf("Service = %q, want one of the two git services", req.Service)
		}
	}
}

func TestGitProtocol(t *testing.T) {
	if got := gitProtocol([]string{"LANG=C", "GIT_PROTOCOL=version=2"}); got != "version=2" {
		t.Errorf("gitProtocol = %q, want version=2", got)
	}
	if got := gitProtocol([]string{"LANG=C"}); got != "" {
		t.Errorf("gitProtocol with no request = %q, want empty", got)
	}
}
