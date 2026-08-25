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
	if err := runAdmin(context.Background(), st, []string{"passwd", "alice"}, &out); err != nil {
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
	err := runAdmin(context.Background(), st, []string{"passwd", "alice"}, &out)
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
	if err := runAdmin(context.Background(), st, []string{"passwd", "alice"}, &out); err == nil {
		t.Fatal("an empty stdin set an empty password")
	}
}

func TestAdminPromote_GrantsSiteAdministratorRights(t *testing.T) {
	st := adminTestStore(t)
	adminTestUser(t, st, "alice", "forgotten forever", false)

	var out bytes.Buffer
	if err := runAdmin(context.Background(), st, []string{"promote", "alice"}, &out); err != nil {
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
	if err := runAdmin(context.Background(), st, []string{"promote", "alice"}, &out); err != nil {
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
	if err := runAdmin(ctx, st, []string{"passwd", "alice"}, &out); err != nil {
		t.Fatalf("admin passwd: %v", err)
	}
	if !strings.Contains(out.String(), "suspended") {
		t.Errorf("no warning that the account is still suspended:\n%s", out.String())
	}

	if err := st.SetUserApproval(ctx, "alice", false); err != nil {
		t.Fatalf("un-approve alice: %v", err)
	}
	out.Reset()
	if err := runAdmin(ctx, st, []string{"promote", "alice"}, &out); err != nil {
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
		if err := runAdmin(context.Background(), st, args, &out); err == nil {
			t.Errorf("runAdmin(%q) succeeded, want an error", args)
		}
	}
}
