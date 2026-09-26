package tfcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
)

// decodeOneJSONLine checks that out is exactly one JSON line and decodes it.
func decodeOneJSONLine(t *testing.T, out string, v any) {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout is not exactly one line: %q", out)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
}

func TestVersionJSON(t *testing.T) {
	code, out, errOut := runMain(t, []string{"version", "--json"}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var v versionJSON
	decodeOneJSONLine(t, out, &v)
	if v.Version != Version || v.OS != runtime.GOOS || v.Arch != runtime.GOARCH || v.GoVersion != runtime.Version() {
		t.Errorf("version = %+v", v)
	}

	// Human output is unchanged, and other arguments are still refused.
	code, out, _ = runMain(t, []string{"version"}, "")
	if code != 0 || out != "tf "+Version+" ("+runtime.GOOS+"/"+runtime.GOARCH+")\n" {
		t.Errorf("human: %d %q", code, out)
	}
	if code, _, _ := runMain(t, []string{"version", "--json", "extra"}, ""); code != 2 {
		t.Errorf("extra argument: exit %d, want 2", code)
	}
}

func TestWhoamiJSON(t *testing.T) {
	isolateEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name": "alice", "fullname": "Alice", "email": "a@example.com",
			"orgs": []map[string]string{
				{"name": "acme", "roleInOrg": "write"},
				{"name": "ro", "roleInOrg": "read"},
			},
			"auth": map[string]any{"accessToken": map[string]any{"role": "write"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, errOut := runMain(t, []string{"whoami", "--json", "--endpoint", srv.URL, "--token", "secret-tok"}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var w whoamiJSON
	decodeOneJSONLine(t, out, &w)
	if w.Name != "alice" || w.Email != "a@example.com" || w.Scope != "write" || !strings.HasPrefix(w.Endpoint, "http://127.0.0.1") {
		t.Errorf("whoami = %+v", w)
	}
	if !reflect.DeepEqual(w.PushTo, []string{"alice", "acme"}) || len(w.Orgs) != 2 || w.Orgs[1] != (statusOrgJSON{Name: "ro", Role: "read"}) {
		t.Errorf("orgs / push_to = %+v / %v", w.Orgs, w.PushTo)
	}
	if strings.Contains(out, "secret-tok") {
		t.Error("the token must never be printed")
	}
}

func TestLoginJSONNeverPrintsTheToken(t *testing.T) {
	isolateEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"id": 42, "name": "n", "scope": "write", "token": "minted-secret"})
	})
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, errOut := runMain(t, []string{"login", srv.URL, "--username", "alice", "--password-stdin", "--json"}, "pw\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var l loginJSON
	decodeOneJSONLine(t, out, &l)
	if l.Username != "alice" || l.Scope != "write" || l.TokenID != 42 || !l.Minted || l.ConfigPath == "" {
		t.Errorf("login = %+v", l)
	}
	if strings.Contains(out, "minted-secret") || strings.Contains(errOut, "minted-secret") {
		t.Error("the token must never be printed")
	}

	// A pasted token: minted false, token_id 0.
	code, out, errOut = runMain(t, []string{"login", srv.URL, "--token", "pasted-secret", "--json"}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	decodeOneJSONLine(t, out, &l)
	if l.Minted || l.TokenID != 0 || strings.Contains(out, "pasted-secret") {
		t.Errorf("pasted login = %s", out)
	}
}

func TestLogoutJSON(t *testing.T) {
	isolateEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/tokens/7", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	normalized, err := config.NormalizeEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	saveCredential(t, config.Credential{Endpoint: normalized, Token: "tok", TokenID: 7, CreatedAt: time.Now()})
	code, out, errOut := runMain(t, []string{"logout", srv.URL, "--json"}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var l logoutJSON
	decodeOneJSONLine(t, out, &l)
	if l != (logoutJSON{Endpoint: normalized, Revoked: true}) {
		t.Errorf("logout = %+v", l)
	}

	// A revoke failure is reported in the JSON too (and stays non-fatal).
	saveCredential(t, config.Credential{Endpoint: normalized, Token: "tok", TokenID: 8, CreatedAt: time.Now()})
	code, out, _ = runMain(t, []string{"logout", srv.URL, "--json"}, "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	l = logoutJSON{}
	decodeOneJSONLine(t, out, &l)
	if l.Revoked || l.RevokeError == "" {
		t.Errorf("logout with failed revoke = %+v", l)
	}
}
