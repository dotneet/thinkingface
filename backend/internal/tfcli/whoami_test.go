package tfcli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhoamiNoEndpointAtAll(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"whoami"}, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "no endpoint") {
		t.Errorf("stderr = %q, want it to mention the missing endpoint", errOut)
	}
}

func TestWhoamiEndpointButNoToken(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"whoami", "--endpoint", "http://example.invalid"}, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "not logged in") {
		t.Errorf("stderr = %q, want it to mention not being logged in", errOut)
	}
}

func TestWhoamiRejectsArguments(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"whoami", "extra"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
}

func TestWhoamiHappyPath(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     "alice",
			"fullname": "Alice Example",
			"email":    "alice@example.com",
			"orgs": []map[string]string{
				{"name": "acme", "fullname": "ACME", "roleInOrg": "admin"},
				{"name": "readonly-org", "fullname": "RO", "roleInOrg": "read"},
			},
			"auth": map[string]any{"accessToken": map[string]any{"role": "write"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, errOut := runMain(t, []string{"whoami", "--endpoint", srv.URL, "--token", "tok"}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "alice (Alice Example) <alice@example.com>") {
		t.Errorf("stdout missing identity line: %q", out)
	}
	if !strings.Contains(out, "token scope: write") {
		t.Errorf("stdout missing scope line: %q", out)
	}
	if !strings.Contains(out, "acme (admin)") {
		t.Errorf("stdout missing org line: %q", out)
	}
	if !strings.Contains(out, "readonly-org (read)") {
		t.Errorf("stdout should list every org membership, including read-only ones: %q", out)
	}
	var pushLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "You can push to:") {
			pushLine = line
		}
	}
	if pushLine != "You can push to: alice, acme" {
		t.Errorf("push line = %q, want it to list alice and acme only (readonly-org excluded)", pushLine)
	}
}
