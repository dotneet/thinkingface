package tfcli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func runMain(t *testing.T, args []string, stdin string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Main(args, strings.NewReader(stdin), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestMainNoArgsPrintsUsageToStdout(t *testing.T) {
	code, out, errOut := runMain(t, nil, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("stdout missing usage text: %q", out)
	}
	if errOut != "" {
		t.Errorf("stderr should be empty, got %q", errOut)
	}
}

func TestMainTopLevelHelpVariants(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		code, out, _ := runMain(t, args, "")
		if code != 0 {
			t.Errorf("%v: exit code = %d, want 0", args, code)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%v: stdout missing usage text: %q", args, out)
		}
	}
}

func TestMainHelpForSubcommand(t *testing.T) {
	code, out, _ := runMain(t, []string{"help", "up"}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "tf up PATH") {
		t.Errorf("stdout missing up usage: %q", out)
	}
}

func TestMainHelpForUnknownSubcommand(t *testing.T) {
	code, _, errOut := runMain(t, []string{"help", "bogus"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "bogus") {
		t.Errorf("stderr should mention the unknown command: %q", errOut)
	}
}

func TestMainSubcommandHelpFlag(t *testing.T) {
	for _, cmd := range []string{"login", "logout", "whoami", "up", "version"} {
		code, out, errOut := runMain(t, []string{cmd, "--help"}, "")
		if code != 0 {
			t.Errorf("%s --help: exit code = %d, want 0", cmd, code)
		}
		if out == "" {
			t.Errorf("%s --help: stdout is empty", cmd)
		}
		if errOut != "" {
			t.Errorf("%s --help: stderr should be empty, got %q", cmd, errOut)
		}
	}
}

func TestMainUnknownCommand(t *testing.T) {
	code, _, errOut := runMain(t, []string{"frobnicate"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q, want it to mention unknown command", errOut)
	}
}

func TestMainVersion(t *testing.T) {
	old := Version
	Version = "v1.2.3"
	defer func() { Version = old }()

	code, out, errOut := runMain(t, []string{"version"}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "tf v1.2.3") {
		t.Errorf("stdout = %q, want it to contain the version", out)
	}
	if errOut != "" {
		t.Errorf("stderr should be empty, got %q", errOut)
	}
}

func TestMainVersionRejectsArguments(t *testing.T) {
	code, _, errOut := runMain(t, []string{"version", "extra"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if errOut == "" {
		t.Error("expected a usage error message on stderr")
	}
}

func TestHasHelpFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"foo"}, false},
		{[]string{"-h"}, true},
		{[]string{"--help"}, true},
		{[]string{"foo", "--help"}, true},
		{[]string{"--", "--help"}, false}, // terminator hides anything after it
	}
	for _, c := range cases {
		if got := hasHelpFlag(c.args); got != c.want {
			t.Errorf("hasHelpFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1200, "1.2 kB"},
		{123_400_000, "123.4 MB"},
		{2_000_000_000, "2.0 GB"},
	}
	for _, c := range cases {
		if got := formatSize(c.n); got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestShortOID(t *testing.T) {
	if got := shortOID("abc1234567890"); got != "abc1234" {
		t.Errorf("shortOID = %q, want abc1234", got)
	}
	if got := shortOID("abc"); got != "abc" {
		t.Errorf("shortOID of a short string should be unchanged, got %q", got)
	}
}

// TestReadLinePreservesInnerAndSurroundingWhitespace is the regression test
// for --password-stdin / --token - silently changing the value being logged
// in with: readLine used to run the line through strings.TrimSpace, so a
// password or token with a leading/trailing space (a legitimate character in
// either) came out different from what was piped in, and the mismatch then
// surfaced as an opaque authentication failure rather than here. Like
// `docker login --password-stdin`, only the trailing newline (and a
// preceding \r, for CRLF input) should be stripped.
func TestReadLinePreservesInnerAndSurroundingWhitespace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"leading and trailing spaces, LF", " hunter2 \n", " hunter2 "},
		{"leading and trailing spaces, CRLF", " hunter2 \r\n", " hunter2 "},
		{"internal whitespace preserved", "a b\tc\n", "a b\tc"},
		{"no trailing newline (last line, EOF)", "hunter2", "hunter2"},
		{"empty line", "\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readLine(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("readLine(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("readLine(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadLineEmptyInputIsUnexpectedEOF(t *testing.T) {
	if _, err := readLine(strings.NewReader("")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("readLine(\"\") err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestMaskToken(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"short":                  "****",
		"tf_abcdefghijklmnopqrs": "tf_…pqrs",
	}
	for in, want := range cases {
		if got := maskToken(in); got != want {
			t.Errorf("maskToken(%q) = %q, want %q", in, got, want)
		}
	}
}
