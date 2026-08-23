package tfcli

import (
	"bytes"
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
