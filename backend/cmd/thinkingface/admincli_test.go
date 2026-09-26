package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// The break-glass commands are tested against a real store rather than a
// fake. What is worth proving here is that the write actually lands -- that
// after `admin passwd` the new password verifies against the stored hash and
// the old one does not -- and a fake that records the call would prove
// nothing about that.

func adminTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func adminTestUser(t *testing.T, st *store.Store, name, password string, isAdmin bool) *store.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u, err := st.CreateUser(context.Background(), name, name+"@example.com", hash, isAdmin)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// withStdin points os.Stdin at a pipe holding the given bytes, which is how
// the non-terminal branch of readNewPassword is reached. A file (rather than
// an in-memory reader) is unavoidable: term.IsTerminal takes a file
// descriptor, so the test has to hand the command a real one.
func withStdin(t *testing.T, data string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("rewind stdin: %v", err)
	}
	saved := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = saved
		_ = f.Close()
	})
}

func TestAdminPasswd_ReplacesTheStoredCredential(t *testing.T) {
	st := adminTestStore(t)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)

	// A trailing newline is stripped: `echo` adds one, and people use `echo`.
	withStdin(t, "a brand new passphrase\n")
	var out bytes.Buffer
	if err := runAdmin(context.Background(), st, []string{"passwd", "alice"}, &out, &out); err != nil {
		t.Fatalf("admin passwd: %v", err)
	}

	fresh, err := st.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("reload alice: %v", err)
	}
	if err := auth.CheckPassword(fresh.PasswordHash, "a brand new passphrase"); err != nil {
		t.Fatalf("the new password does not verify against the stored hash: %v", err)
	}
	if auth.CheckPassword(fresh.PasswordHash, "forgotten forever") == nil {
		t.Fatal("the old password still verifies")
	}
	// The same invariant UpdateUserPassword carries for every other caller:
	// changing a password revokes the sessions minted from it, in the same
	// statement. The reason somebody is running this may be a stolen cookie.
	if fresh.SessionEpoch != alice.SessionEpoch+1 {
		t.Errorf("session_epoch = %d, want %d: the reset did not revoke sessions",
			fresh.SessionEpoch, alice.SessionEpoch+1)
	}
	// It says who it acted on. An operator running this in an emergency has
	// to be able to see they did not fix the wrong account.
	if got := out.String(); !strings.Contains(got, "alice") || !strings.Contains(got, "signed out") {
		t.Errorf("output does not report what happened to whom:\n%s", got)
	}
}

func TestAdminPasswd_RefusesAPasswordTheWebUIWouldRefuse(t *testing.T) {
	st := adminTestStore(t)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)

	withStdin(t, "short\n")
	var out bytes.Buffer
	err := runAdmin(context.Background(), st, []string{"passwd", "alice"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("admin passwd with a 5-character password = %v, want the length policy", err)
	}
	// Nothing was written: a command that refuses must leave the account it
	// refused on exactly as it found it.
	fresh, err := st.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("reload alice: %v", err)
	}
	if auth.CheckPassword(fresh.PasswordHash, "forgotten forever") != nil {
		t.Error("the old password stopped working after a refused reset")
	}
	if fresh.SessionEpoch != alice.SessionEpoch {
		t.Error("a refused reset revoked sessions anyway")
	}
}

func TestAdminPasswd_EmptyStdinIsAnError(t *testing.T) {
	st := adminTestStore(t)
	adminTestUser(t, st, "alice", "forgotten forever", false)

	withStdin(t, "")
	var out bytes.Buffer
	if err := runAdmin(context.Background(), st, []string{"passwd", "alice"}, &out, &out); err == nil {
		t.Fatal("an empty stdin set an empty password")
	}
}

func TestAdminPromote_GrantsSiteAdministratorRights(t *testing.T) {
	st := adminTestStore(t)
	adminTestUser(t, st, "alice", "forgotten forever", false)

	var out bytes.Buffer
	if err := runAdmin(context.Background(), st, []string{"promote", "alice"}, &out, &out); err != nil {
		t.Fatalf("admin promote: %v", err)
	}
	fresh, err := st.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("reload alice: %v", err)
	}
	if !fresh.IsAdmin {
		t.Fatal("alice is not a site administrator after promote")
	}
	if got := out.String(); !strings.Contains(got, "alice") {
		t.Errorf("output does not name the account:\n%s", got)
	}

	// Running it again is not an error. Whoever is here is unsure what the
	// database holds, and "already an administrator" is a more useful answer
	// than either a silent success or a failure.
	out.Reset()
	if err := runAdmin(context.Background(), st, []string{"promote", "alice"}, &out, &out); err != nil {
		t.Fatalf("admin promote (repeat): %v", err)
	}
	if !strings.Contains(out.String(), "already") {
		t.Errorf("a repeated promote does not say so:\n%s", out.String())
	}
}

// The account this command just fixed may still be barred by one of the two
// gates, and both are invisible from outside -- a suspended or unapproved
// account answers a *correct* password with a refusal. Saying so is the
// difference between fixing the instance and thinking you did.
func TestAdminCLI_WarnsWhenTheAccountStillCannotSignIn(t *testing.T) {
	ctx := context.Background()
	st := adminTestStore(t)
	adminTestUser(t, st, "root", "correct horse battery", true)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)
	if err := st.SetUserDisabled(ctx, "alice", true, alice.ID); err != nil {
		t.Fatalf("suspend alice: %v", err)
	}

	withStdin(t, "a brand new passphrase\n")
	var out bytes.Buffer
	if err := runAdmin(ctx, st, []string{"passwd", "alice"}, &out, &out); err != nil {
		t.Fatalf("admin passwd: %v", err)
	}
	if !strings.Contains(out.String(), "suspended") {
		t.Errorf("no warning that the account is still suspended:\n%s", out.String())
	}

	if err := st.SetUserApproval(ctx, "alice", false); err != nil {
		t.Fatalf("un-approve alice: %v", err)
	}
	out.Reset()
	if err := runAdmin(ctx, st, []string{"promote", "alice"}, &out, &out); err != nil {
		t.Fatalf("admin promote: %v", err)
	}
	if !strings.Contains(out.String(), "approval") {
		t.Errorf("no warning that the account is waiting for approval:\n%s", out.String())
	}
}

func TestAdminCLI_RejectsBadInvocations(t *testing.T) {
	st := adminTestStore(t)
	adminTestUser(t, st, "alice", "forgotten forever", false)

	cases := [][]string{
		{},
		{"passwd"},
		{"promote"},
		{"frobnicate", "alice"},
		{"promote", "alice", "extra"},
		{"promote", "nobody"},
	}
	for _, args := range cases {
		var out bytes.Buffer
		if err := runAdmin(context.Background(), st, args, &out, &out); err == nil {
			t.Errorf("runAdmin(%q) succeeded, want an error", args)
		}
	}
}

// adminTestRepo creates a repository row; the token commands only ever look
// repositories up, so no git directory is needed.
func adminTestRepo(t *testing.T, st *store.Store, ns, name, kind string) *store.Repo {
	t.Helper()
	ctx := context.Background()
	n, err := st.GetNamespace(ctx, ns)
	if err != nil {
		t.Fatalf("namespace %s: %v", ns, err)
	}
	r, err := st.CreateRepo(ctx, n.ID, name, kind, "", "main", store.NewStoragePath())
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return r
}

// The token on stdout is the whole of stdout, and it authenticates as the
// account it was minted for with the restriction it was minted with.
func TestAdminTokenCreate_PrintsOnlyTheToken(t *testing.T) {
	st := adminTestStore(t)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)
	exp := adminTestRepo(t, st, "alice", "exp", "dataset")

	var out, errOut bytes.Buffer
	err := runAdmin(context.Background(), st,
		[]string{"token", "create", "alice", "--name", "agent", "--expires-in-days", "7", "--repo", "datasets/alice/exp"},
		&out, &errOut)
	if err != nil {
		t.Fatalf("admin token create: %v (stderr %s)", err, errOut.String())
	}
	token := strings.TrimSuffix(out.String(), "\n")
	if !strings.HasPrefix(token, auth.TokenPrefix) || strings.ContainsAny(token, " \n") {
		t.Fatalf("stdout = %q, want exactly one token line", out.String())
	}
	if !strings.Contains(errOut.String(), `"agent"`) || !strings.Contains(errOut.String(), "datasets/alice/exp") {
		t.Errorf("stderr report = %q, want the name and the restriction", errOut.String())
	}

	u, tok, err := st.LookupToken(context.Background(), auth.HashToken(token))
	if err != nil || u.ID != alice.ID {
		t.Fatalf("minted token does not authenticate as alice: %v", err)
	}
	if tok.Name != "agent" || tok.Scope != "write" {
		t.Errorf("token = %+v, want agent/write", tok)
	}
	r, err := st.LookupTokenRestriction(context.Background(), tok.ID)
	if err != nil || !r.Restricted || len(r.RepoIDs) != 1 || r.RepoIDs[0] != exp.ID {
		t.Errorf("restriction = %+v, %v; want restricted to %d", r, err, exp.ID)
	}
	list, err := st.ListTokens(context.Background(), alice.ID)
	if err != nil || len(list) != 1 || list[0].ExpiresAt == nil {
		t.Errorf("tokens = %+v, %v; want one with an expiry", list, err)
	}
}

// Defaults: name admin-cli, write scope, no expiry, unrestricted -- and the
// username may follow the flags.
func TestAdminTokenCreate_Defaults(t *testing.T) {
	st := adminTestStore(t)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)

	var out, errOut bytes.Buffer
	if err := runAdmin(context.Background(), st, []string{"token", "create", "--scope", "write", "alice"}, &out, &errOut); err != nil {
		t.Fatalf("admin token create: %v", err)
	}
	list, err := st.ListTokens(context.Background(), alice.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("tokens = %+v, %v", list, err)
	}
	if got := list[0]; got.Name != "admin-cli" || got.Scope != "write" || got.ExpiresAt != nil || len(got.Repos) != 0 {
		t.Errorf("token = %+v, want admin-cli / write / no expiry / unrestricted", got)
	}
}

// --output writes the token to a new 0600 file and nothing to stdout, and
// refuses -- before minting anything -- a path that already exists.
func TestAdminTokenCreate_OutputFile(t *testing.T) {
	st := adminTestStore(t)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)
	path := filepath.Join(t.TempDir(), "token")

	var out, errOut bytes.Buffer
	if err := runAdmin(context.Background(), st, []string{"token", "create", "alice", "--output", path}, &out, &errOut); err != nil {
		t.Fatalf("admin token create: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing when --output is given", out.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), auth.TokenPrefix) {
		t.Errorf("file = %q, want the token", data)
	}

	// Again at the same path: refused, file untouched, no second token.
	err = runAdmin(context.Background(), st, []string{"token", "create", "alice", "--output", path}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second run error = %v, want already exists", err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(data) {
		t.Error("an existing output file was overwritten")
	}
	if list, _ := st.ListTokens(context.Background(), alice.ID); len(list) != 1 {
		t.Errorf("%d tokens after a refused run, want 1", len(list))
	}
}

// The validation is the HTTP handler's: a refused request mints nothing and
// leaves no output file behind.
func TestAdminTokenCreate_Refusals(t *testing.T) {
	st := adminTestStore(t)
	alice := adminTestUser(t, st, "alice", "forgotten forever", false)
	adminTestUser(t, st, "bob", "forgotten forever", false)
	adminTestRepo(t, st, "bob", "theirs", "dataset")
	dir := t.TempDir()

	cases := [][]string{
		{"token"},
		{"token", "list", "alice"},
		{"token", "create"},
		{"token", "create", "nobody"},
		{"token", "create", "alice", "extra"},
		{"token", "create", "alice", "--scope", "admin"},
		{"token", "create", "alice", "--expires-in-days", "-1"},
		{"token", "create", "alice", "--expires-in-days", "366"},
		{"token", "create", "alice", "--repo", "alice/exp"},
		{"token", "create", "alice", "--repo", "datasets/alice/missing"},
		{"token", "create", "alice", "--repo", "datasets/bob/theirs"},
		{"token", "create", "alice", "--scope", "read", "--repo", "datasets/bob/theirs"},
		{"token", "create", "alice", "--repo", "datasets/bob/theirs", "--output", filepath.Join(dir, "refused")},
	}
	for _, args := range cases {
		var out, errOut bytes.Buffer
		if err := runAdmin(context.Background(), st, args, &out, &errOut); err == nil {
			t.Errorf("runAdmin(%q) succeeded, want an error", args)
		}
		if out.Len() != 0 {
			t.Errorf("runAdmin(%q) wrote %q to stdout", args, out.String())
		}
	}
	if list, _ := st.ListTokens(context.Background(), alice.ID); len(list) != 0 {
		t.Errorf("%d tokens minted by refused invocations", len(list))
	}
	if _, err := os.Stat(filepath.Join(dir, "refused")); !os.IsNotExist(err) {
		t.Errorf("a refused run left its output file behind: %v", err)
	}
}
