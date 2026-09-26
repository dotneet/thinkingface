package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/dotneet/thinkingface/backend/internal/api"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// The break-glass subcommands: `thinkingface admin passwd` and
// `thinkingface admin promote` -- plus `thinkingface admin token create`, which
// mints an access token without a browser (docs/dev/agent-features.md §1.4).
//
// Everything else in this server assumes somebody can already sign in. A
// forgotten password is reset by a site administrator at PATCH
// /api/v1/admin/users/{username}, and that endpoint accepts a *session
// cookie* and nothing else -- so on an instance with one administrator, one
// forgotten password used to mean the only remaining repair was editing the
// database by hand. TF_ADMIN_PASSWORD does not help either: seedAdmin runs
// only while the users table is empty.
//
// These commands are that repair, done properly. They run out of process
// against the same database the server uses, exactly as `gc` and `resync` do,
// so they need shell access to the deployment -- which is the authorization
// story: whoever can run this can already read the database.
//
// The password is never an argument. A password on a command line is in the
// shell history of whoever typed it and in /proc for every user on the box
// for as long as the process lives, which for a bcrypt hash is long enough to
// matter. It is read from the terminal without echo, or from stdin when there
// is no terminal (`printf '%s' "$pw" | thinkingface admin passwd alice`), so
// a configuration-management run can do this unattended.

// adminUsage is printed for a malformed invocation. passwd and promote are
// deliberately not a flag.FlagSet: they take no flags, and an -h that listed
// none would only suggest there were some. token create is the one that does.
const adminUsage = `usage:
  thinkingface admin passwd <username>    reset a password (read from the terminal or stdin)
  thinkingface admin promote <username>   grant site administrator rights
  thinkingface admin token create <username> [--name NAME] [--scope read|write]
      [--expires-in-days N] [--repo KIND/NS/NAME ...] [--output FILE]
                                          mint an access token; only the token goes to stdout
                                          (or to FILE, created 0600 and never overwritten)`

// adminDB is the store surface runAdmin needs. *store.Store implements it,
// and naming it here keeps the commands testable against a real store without
// dragging the rest of the server in.
type adminDB interface {
	api.TokenMintStore
	GetUserByUsername(ctx context.Context, username string) (*store.User, error)
	UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) (int64, error)
	SetUserAdmin(ctx context.Context, userID int64, isAdmin bool) error
}

// runAdmin dispatches the `admin` subcommands. out is where the report goes;
// main passes os.Stdout. errOut is os.Stderr, which only `token create` uses:
// its stdout carries the token and nothing else, so its report goes there.
//
// The output is plain text on stdout rather than slog JSON, because a human
// is standing at the terminal reading it -- and because the whole point of
// the command is to tell them exactly what it did to whom.
func runAdmin(ctx context.Context, db adminDB, args []string, out, errOut io.Writer) error {
	if len(args) >= 1 && args[0] == "token" {
		return adminToken(ctx, db, args[1:], out, errOut)
	}
	if len(args) < 2 {
		return errors.New(adminUsage)
	}
	verb, username := args[0], args[1]
	if len(args) > 2 {
		return fmt.Errorf("unexpected argument %q\n%s", args[2], adminUsage)
	}
	switch verb {
	case "passwd":
		return adminPasswd(ctx, db, username, out)
	case "promote":
		return adminPromote(ctx, db, username, out)
	default:
		return fmt.Errorf("unknown admin command %q\n%s", verb, adminUsage)
	}
}

// adminPasswd resets an account's password.
//
// It goes through store.UpdateUserPassword rather than writing the column
// itself, so it inherits the invariant that write carries: the new hash and
// the session_epoch bump land in one statement, and every cookie already
// issued for the account stops working. That matters more here than anywhere
// else -- the reason somebody is running this may well be that a session was
// stolen.
//
// Access tokens are deliberately left alone, exactly as they are for every
// other password change in this server (docs/dev/api-contract.md §1.3). A
// forgotten password is not evidence about a token. Revoking them is a
// separate, deliberate action.
func adminPasswd(ctx context.Context, db adminDB, username string, out io.Writer) error {
	user, err := db.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no account named %q", username)
		}
		return fmt.Errorf("load account %q: %w", username, err)
	}

	password, err := readNewPassword(os.Stdin, out)
	if err != nil {
		return err
	}
	// The same policy the HTTP routes apply, restated rather than imported:
	// api.validatePassword is unexported, and a command that could set a
	// password the web UI would refuse to accept is a trap for whoever uses
	// it next.
	if len(password) < minPasswordBytes {
		return fmt.Errorf("password must be at least %d characters", minPasswordBytes)
	}
	if len(password) > auth.MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", auth.MaxPasswordBytes)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := db.UpdateUserPassword(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	fmt.Fprintf(out, "Password reset for %s (user id %d, %s).\n", user.Username, user.ID, user.Email)
	fmt.Fprintln(out, "Every session that account had open has been signed out.")
	fmt.Fprintln(out, "Its access tokens and SSH keys are untouched, as they are for any password change.")
	warnAboutGates(user, out)
	return nil
}

// adminPromote grants site administrator rights.
//
// This is the other half of the break-glass story: an instance whose only
// administrator is gone needs somebody else to become one, and the endpoint
// that does it requires an administrator's session. There is no matching
// `demote` on purpose -- revoking rights is not an emergency, it is ordinary
// administration, and it already has a screen and a last-administrator guard
// that this command would have to reimplement.
func adminPromote(ctx context.Context, db adminDB, username string, out io.Writer) error {
	user, err := db.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no account named %q", username)
		}
		return fmt.Errorf("load account %q: %w", username, err)
	}
	if user.IsAdmin {
		// Not an error: the requested state is the state. Saying so is more
		// useful than a silent success, since the operator is here precisely
		// because they are unsure what the database holds.
		fmt.Fprintf(out, "%s (user id %d) is already a site administrator; nothing to do.\n",
			user.Username, user.ID)
		warnAboutGates(user, out)
		return nil
	}
	if err := db.SetUserAdmin(ctx, user.ID, true); err != nil {
		return fmt.Errorf("grant site administrator rights: %w", err)
	}
	fmt.Fprintf(out, "%s (user id %d, %s) is now a site administrator.\n",
		user.Username, user.ID, user.Email)
	fmt.Fprintln(out, "They can manage every account at /settings/admin/users after signing in.")
	warnAboutGates(user, out)
	return nil
}

// adminToken implements `thinkingface admin token create`: the way to hand an
// automated client (an agent, a CI job) a token on an instance nobody has
// signed into yet, or from provisioning that has no browser.
//
// The validation is api.MintToken's -- the same function POST /api/v1/tokens
// calls -- so this cannot mint a token the web UI would refuse: the same scope
// and expiry rules, and a --repo list whose every entry must exist and be
// writable by the account the token is for.
//
// stdout receives the token and nothing else, so `TOKEN=$(thinkingface admin
// token create alice)` works; what was done is reported on errOut. With
// --output the token goes to a file created 0600 with O_EXCL instead, and an
// existing file is refused before any token is minted -- overwriting whatever
// is there, or following a symlink someone planted, is not something a
// credential writer should ever do.
func adminToken(ctx context.Context, db adminDB, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New(adminUsage)
	}
	fs := flag.NewFlagSet("admin token create", flag.ContinueOnError)
	fs.SetOutput(errOut)
	name := fs.String("name", "admin-cli", "a label for the token, shown in the token list")
	scope := fs.String("scope", "write", `"read" or "write"`)
	days := fs.Int("expires-in-days", 0, "days until the token expires (0 = never)")
	var repos repoList
	fs.Var(&repos, "repo", "restrict the token to this repository, as datasets/NS/NAME or models/NS/NAME (repeatable)")
	output := fs.String("output", "", "write the token to this file (created 0600; refused if it exists) instead of stdout")

	// The username may come before or after the flags: flag stops at the
	// first positional argument, so a leading one is taken off first.
	rest := args[1:]
	var username string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		username, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	extra := fs.Args()
	if username == "" {
		if len(extra) == 0 {
			return fmt.Errorf("a username is required\n%s", adminUsage)
		}
		username, extra = extra[0], extra[1:]
	}
	if len(extra) > 0 {
		return fmt.Errorf("unexpected argument %q\n%s", extra[0], adminUsage)
	}

	user, err := db.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no account named %q", username)
		}
		return fmt.Errorf("load account %q: %w", username, err)
	}

	// Claimed before the token exists, so a path that is already taken costs
	// nothing: no token is minted that would then have nowhere to go.
	var file *os.File
	if *output != "" {
		file, err = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s already exists; refusing to overwrite it", *output)
			}
			return fmt.Errorf("create %s: %w", *output, err)
		}
	}
	discardFile := func() {
		if file != nil {
			_ = file.Close()
			_ = os.Remove(*output)
		}
	}

	token, rec, err := api.MintToken(ctx, db, user, api.TokenMintRequest{
		Name: *name, Scope: *scope, ExpiresInDays: *days, Repos: repos,
	})
	if err != nil {
		discardFile()
		return err
	}
	if file != nil {
		_, werr := io.WriteString(file, token+"\n")
		if cerr := file.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(*output)
			return fmt.Errorf("write %s: %w (token id %d was created and is unusable; revoke it from /settings/tokens)",
				*output, werr, rec.ID)
		}
	} else if _, err := io.WriteString(out, token+"\n"); err != nil {
		return fmt.Errorf("write token: %w (token id %d was created; revoke it from /settings/tokens)", err, rec.ID)
	}

	// The same audit line the HTTP handler writes, with the actor spelled out:
	// nobody signed in to mint this one.
	slog.Info("access token created", "username", user.Username, "user_id", user.ID,
		"token_id", rec.ID, "token_name", rec.Name, "scope", rec.Scope,
		"repos", len(rec.Repos), "actor", "admin-cli")

	fmt.Fprintf(errOut, "Created access token %q (id %d, scope %s) for %s.\n", rec.Name, rec.ID, rec.Scope, user.Username)
	if len(rec.Repos) > 0 {
		names := make([]string, 0, len(rec.Repos))
		for _, r := range rec.Repos {
			names = append(names, api.TokenRepoSpec(r))
		}
		fmt.Fprintf(errOut, "It may change only: %s.\n", strings.Join(names, ", "))
	}
	if rec.ExpiresAt != nil {
		fmt.Fprintf(errOut, "It expires at %s.\n", rec.ExpiresAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(errOut, "It never expires.")
	}
	if file != nil {
		fmt.Fprintf(errOut, "The token was written to %s (mode 0600).\n", *output)
	}
	warnAboutGates(user, errOut)
	return nil
}

// repoList is the repeatable --repo flag.
type repoList []string

func (l *repoList) String() string { return strings.Join(*l, ",") }

func (l *repoList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// warnAboutGates says so when the account this command just fixed still
// cannot sign in. Both gates are invisible from the outside -- a suspended or
// unapproved account answers a *correct* password with a refusal -- so
// resetting a password and walking away would leave the operator convinced
// the job was done.
func warnAboutGates(user *store.User, out io.Writer) {
	if user.Disabled() {
		fmt.Fprintf(out, "Note: %s is suspended and still cannot sign in. "+
			"Restore it from /settings/admin/users first.\n", user.Username)
	}
	if user.PendingApproval() {
		fmt.Fprintf(out, "Note: %s is waiting for sign-up approval and still cannot sign in. "+
			"Approve it from /settings/admin/users first.\n", user.Username)
	}
}

// minPasswordBytes mirrors api.minPasswordBytes. See adminPasswd.
const minPasswordBytes = 8

// readNewPassword reads the replacement password without ever putting it on a
// command line.
//
// On a terminal it is prompted for twice, with echo off, and the two must
// match -- there is no "forgot password" for the account that fixes forgotten
// passwords, so a typo here is expensive. Piped input is read as a single
// value instead: there is nobody to confirm with, and asking twice would mean
// the caller had to send it twice.
//
// A trailing newline is stripped from piped input (`echo` adds one and people
// use `echo`), and so is a trailing carriage return, but nothing else is: a
// password may legitimately begin or end with a space.
func readNewPassword(in *os.File, out io.Writer) (string, error) {
	if !term.IsTerminal(int(in.Fd())) {
		data, err := io.ReadAll(in)
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if password == "" {
			return "", errors.New("no password on stdin (pipe one in, or run this from a terminal)")
		}
		return password, nil
	}

	fmt.Fprint(out, "New password: ")
	first, err := term.ReadPassword(int(in.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprint(out, "Confirm password: ")
	second, err := term.ReadPassword(int(in.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("the two passwords do not match")
	}
	return string(first), nil
}
