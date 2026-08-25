package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// The break-glass subcommands: `thinkingface admin passwd` and
// `thinkingface admin promote`.
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

// adminUsage is printed for a malformed invocation. It is deliberately not a
// flag.FlagSet: these take no flags, and an -h that listed none would only
// suggest there were some.
const adminUsage = `usage:
  thinkingface admin passwd <username>    reset a password (read from the terminal or stdin)
  thinkingface admin promote <username>   grant site administrator rights`

// adminDB is the store surface runAdmin needs. *store.Store implements it,
// and naming it here keeps the commands testable against a real store without
// dragging the rest of the server in.
type adminDB interface {
	GetUserByUsername(ctx context.Context, username string) (*store.User, error)
	UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) (int64, error)
	SetUserAdmin(ctx context.Context, userID int64, isAdmin bool) error
}

// runAdmin dispatches the `admin` subcommands. out is where the report goes;
// main passes os.Stdout.
//
// The output is plain text on stdout rather than slog JSON, because a human
// is standing at the terminal reading it -- and because the whole point of
// the command is to tell them exactly what it did to whom.
func runAdmin(ctx context.Context, db adminDB, args []string, out io.Writer) error {
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
