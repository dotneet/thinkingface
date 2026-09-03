package tfcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
)

func whoamiHandler(t *testing.T, name, role string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     name,
			"fullname": strings.ToUpper(name[:1]) + name[1:],
			"email":    name + "@example.com",
			"orgs":     []any{},
			"auth":     map[string]any{"accessToken": map[string]any{"role": role}},
		})
	}
}

func TestLoginNoEndpointNonInteractiveIsUsageError(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"login"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
}

func TestLoginTooManyPositionalArgs(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"login", "http://a", "http://b"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
}

func TestLoginWithTokenHappyPath(t *testing.T) {
	configPath := isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{"login", srv.URL, "--token", "secrettoken"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Logged in to") || !strings.Contains(out.String(), "alice") {
		t.Errorf("stdout = %q, want a confirmation mentioning alice", out.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config file was not written: %v", err)
	}
	var f config.File
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("config file is not valid JSON: %v", err)
	}
	normalized, err := config.NormalizeEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := f.Get(normalized)
	if !ok {
		t.Fatalf("no credential saved for %s (have %v)", normalized, f.Credentials)
	}
	if cred.Token != "secrettoken" {
		t.Errorf("saved token = %q, want secrettoken", cred.Token)
	}
	if cred.TokenID != 0 {
		t.Errorf("a pasted --token should be saved with TokenID 0, got %d", cred.TokenID)
	}
	if cred.Username != "alice" {
		t.Errorf("saved username = %q, want alice", cred.Username)
	}
}

func TestLoginWithTokenDashReadsFromStdin(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{"login", srv.URL, "--token", "-"}, strings.NewReader("piped-token\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
}

func TestLoginWithReadOnlyTokenWarns(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "read"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{"login", srv.URL, "--token", "ro-token"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "read-only") {
		t.Errorf("stderr = %q, want a read-only-scope warning", errOut.String())
	}
}

func TestLoginBadTokenFails(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, map[string]any{"error": map[string]string{"type": "unauthorized", "message": "bad token"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{"login", srv.URL, "--token", "bad"}, strings.NewReader(""), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, errOut.String())
	}
}

func TestLoginPasswordFlowMintsAndSavesToken(t *testing.T) {
	configPath := isolateEnv(t)

	var gotLoginUser string
	var gotTokenName string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotLoginUser = req.Username
		if req.Password != "hunter2" {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(t, w, map[string]any{"error": map[string]string{"message": "bad password"}})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s"})
		writeJSON(t, w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Scope string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotTokenName = req.Name
		writeJSON(t, w, map[string]any{"id": 42, "name": req.Name, "scope": req.Scope, "token": "minted-token"})
	})
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{
		"login", srv.URL,
		"--username", "alice",
		"--password-stdin",
		"--name", "my-token",
	}, strings.NewReader("hunter2\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if gotLoginUser != "alice" {
		t.Errorf("login saw username %q, want alice", gotLoginUser)
	}
	if gotTokenName != "my-token" {
		t.Errorf("token name = %q, want my-token (from --name)", gotTokenName)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config file was not written: %v", err)
	}
	var f config.File
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	normalized, _ := config.NormalizeEndpoint(srv.URL)
	cred, ok := f.Get(normalized)
	if !ok {
		t.Fatalf("no credential saved for %s", normalized)
	}
	if cred.Token != "minted-token" {
		t.Errorf("saved token = %q, want minted-token", cred.Token)
	}
	if cred.TokenID != 42 {
		t.Errorf("saved token id = %d, want 42", cred.TokenID)
	}
}

// TestLoginPasswordStdinPreservesSurroundingWhitespace is the regression
// test for a password with a leading or trailing space being unusable via
// --password-stdin: readLine used to run the piped line through
// strings.TrimSpace, so the password thinkingface actually sent to the
// server differed from what was piped in.
func TestLoginPasswordStdinPreservesSurroundingWhitespace(t *testing.T) {
	isolateEnv(t)

	const password = " hunter2 " // leading and trailing space, on purpose
	var gotPassword string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotPassword = req.Password
		if req.Password != password {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(t, w, map[string]any{"error": map[string]string{"message": "bad password"}})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s"})
		writeJSON(t, w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"id": 1, "name": "tok", "scope": "write", "token": "minted-token"})
	})
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{
		"login", srv.URL,
		"--username", "alice",
		"--password-stdin",
	}, strings.NewReader(password+"\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if gotPassword != password {
		t.Errorf("server received password %q, want %q unchanged", gotPassword, password)
	}
}

// The username is trimmed while the password is not, and the two travel
// through the same readLine. A space on the end of a username -- typed at the
// prompt or pasted into --username -- used to reach the server verbatim and
// come back as "username or password is incorrect", which points at the wrong
// field.
func TestLoginTrimsTheUsernameButNotThePassword(t *testing.T) {
	isolateEnv(t)

	const password = " hunter2 "
	var gotUser, gotPassword string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotUser, gotPassword = req.Username, req.Password
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s"})
		writeJSON(t, w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"id": 1, "name": "tok", "scope": "write", "token": "minted-token"})
	})
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := Main([]string{
		"login", srv.URL,
		"--username", "  alice  ",
		"--password-stdin",
	}, strings.NewReader(password+"\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if gotUser != "alice" {
		t.Errorf("server received username %q, want %q", gotUser, "alice")
	}
	if gotPassword != password {
		t.Errorf("server received password %q, want %q unchanged", gotPassword, password)
	}
}

func TestLoginPasswordFlowNonInteractiveWithoutFlagsIsUsageError(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// No --token, no --username, no --password-stdin, and stdin is not a
	// terminal (a strings.Reader never is): tf can't prompt, so this must be
	// a usage error rather than hanging or guessing.
	code, _, errOut := runMain(t, []string{"login", srv.URL}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
}

// TestLoginUsesEndpointFromEnvironment is the regression test for `tf login`
// being the one command that ignored THINKINGFACE_ENDPOINT: docs/dev/tf-cli.md
// promises the same endpoint precedence for every subcommand, and a CI job
// that exports the endpoint should not have to repeat it as an argument.
func TestLoginUsesEndpointFromEnvironment(t *testing.T) {
	configPath := isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("THINKINGFACE_ENDPOINT", srv.URL)

	var out, errOut bytes.Buffer
	code := Main([]string{"login", "--token", "secrettoken", "--verbose"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "env THINKINGFACE_ENDPOINT") {
		t.Errorf("stderr = %q, want --verbose to name the environment variable the endpoint came from", errOut.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config file was not written: %v", err)
	}
	var f config.File
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("config file is not valid JSON: %v", err)
	}
	normalized, err := config.NormalizeEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Get(normalized); !ok {
		t.Fatalf("no credential saved for %s (have %v)", normalized, f.Credentials)
	}
}

// TestLoginArgumentBeatsEnvironment keeps the precedence right: an explicit
// ENDPOINT still wins over the environment.
func TestLoginArgumentBeatsEnvironment(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", whoamiHandler(t, "alice", "write"))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("THINKINGFACE_ENDPOINT", "http://never-contacted.invalid")

	var out, errOut bytes.Buffer
	code := Main([]string{"login", srv.URL, "--token", "secrettoken"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Logged in to") {
		t.Errorf("stdout = %q, want a confirmation", out.String())
	}
}
