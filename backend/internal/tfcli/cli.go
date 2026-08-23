// Package tfcli implements the `tf` command: argument parsing, the login /
// logout / whoami / up / version subcommands, and terminal output. The HTTP
// work lives in hub, the filesystem work in local, credentials in config.
//
// Exit codes: 0 success, 1 failure (message on stderr), 2 usage error.
package tfcli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

// Version is stamped by the build (-ldflags "-X .../tfcli.Version=v1.2.3").
var Version = "dev"

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const topUsage = `tf is the thinkingface command-line client.

Usage:
  tf <command> [flags] [args]

Commands:
  login    Log in to a thinkingface server and save a token
  logout   Forget the saved credentials for a server
  whoami   Show the identity behind the current token
  status   Show the resolved endpoint/token and whether you are logged in
  up       Create (if needed) and push a directory to a repository
  version  Print the tf version
  help     Show help for tf or for one command

Run 'tf help <command>' or 'tf <command> --help' for details on a command.
`

// Main runs the CLI and returns the process exit code. args excludes the
// program name. stdin is used for prompts (login); stdout carries results,
// stderr progress and errors.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, topUsage)
		return exitOK
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "-h", "--help":
		fmt.Fprint(stdout, topUsage)
		return exitOK
	case "help":
		return runHelp(rest, stdout, stderr)
	case "login":
		return runLogin(rest, stdin, stdout, stderr)
	case "logout":
		return runLogout(rest, stdin, stdout, stderr)
	case "whoami":
		return runWhoami(rest, stdin, stdout, stderr)
	case "status":
		return runStatus(rest, stdout, stderr)
	case "up":
		return runUp(rest, stdin, stdout, stderr)
	case "version":
		return runVersion(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tf: unknown command %q\n", cmd)
		fmt.Fprint(stderr, topUsage)
		return exitUsage
	}
}

// runHelp implements `tf help` and `tf help <command>`.
func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, topUsage)
		return exitOK
	}
	usage, ok := commandUsage(args[0])
	if !ok {
		fmt.Fprintf(stderr, "tf: unknown command %q\n", args[0])
		fmt.Fprint(stderr, topUsage)
		return exitUsage
	}
	fmt.Fprint(stdout, usage)
	return exitOK
}

// commandUsage returns the detailed usage text for one subcommand name.
func commandUsage(name string) (string, bool) {
	switch name {
	case "login":
		return loginUsage, true
	case "logout":
		return logoutUsage, true
	case "whoami":
		return whoamiUsage, true
	case "status":
		return statusUsage, true
	case "up":
		return upUsage, true
	case "version":
		return versionUsage, true
	case "help":
		return topUsage, true
	}
	return "", false
}

// hasHelpFlag reports whether args asks for help before any "--" terminator.
// Subcommands check this ahead of flag.FlagSet.Parse so that -h/--help can be
// routed to stdout with exit 0, distinct from a genuine usage error.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// parseInterspersed parses args into fs, allowing positional arguments to
// appear before, between, or after flags. The stdlib flag package only
// supports flags before positionals: as soon as Parse sees a token that
// doesn't start with "-", it stops and treats everything from there on as
// positional, even further "--flags" (this is why `tf up DIR --json` would
// otherwise fail to see --json). Re-invoking Parse each time that happens
// lets it keep consuming flags after each positional.
func parseInterspersed(fs *flag.FlagSet, args []string) (positionals []string, err error) {
	remaining := args
	for {
		if err := fs.Parse(remaining); err != nil {
			return positionals, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, rest[0])
		remaining = rest[1:]
	}
}

// commonFlags are accepted by every subcommand.
type commonFlags struct {
	endpoint string
	token    string
	verbose  bool
}

// addCommonFlags registers --endpoint/--token/--api-key/--verbose on fs.
// --api-key is an alias of --token: the value is the same personal access
// token, named the way THINKINGFACE_API_KEY names it.
func addCommonFlags(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.endpoint, "endpoint", "", "server URL")
	fs.StringVar(&c.token, "token", "", "API token")
	fs.StringVar(&c.token, "api-key", "", "API token (alias of --token)")
	fs.BoolVar(&c.verbose, "verbose", false, "print credential resolution to stderr")
	return c
}

// resolveCreds loads the config file and resolves the endpoint/token to use,
// printing provenance to stderr when verbose is set.
func resolveCreds(c *commonFlags, stderr io.Writer) (config.Resolved, error) {
	file, err := config.Load()
	if err != nil {
		return config.Resolved{}, fmt.Errorf("loading config: %w", err)
	}
	resolved, err := config.Resolve(c.endpoint, c.token, os.Getenv, file)
	if err != nil {
		return config.Resolved{}, err
	}
	if c.verbose {
		fmt.Fprintf(stderr, "tf: endpoint %s (from %s)\n", resolved.Endpoint, resolved.EndpointSource)
		if resolved.Token != "" {
			fmt.Fprintf(stderr, "tf: token from %s\n", resolved.TokenSource)
		} else {
			fmt.Fprintln(stderr, "tf: no token")
		}
	}
	return resolved, nil
}

// userAgent is the User-Agent every hub.Client this CLI builds sends.
func userAgent() string {
	return "thinkingface-tf/" + Version
}

// readLine reads one line from r, trimming the trailing newline (and a
// preceding carriage return). io.EOF with no data yet read is still an error
// here: callers need a value, not silence.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if err != nil && line == "" {
		return "", io.ErrUnexpectedEOF
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return strings.TrimSpace(line), nil
}

// isTerminalReader reports whether r is an *os.File connected to a terminal.
func isTerminalReader(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isTerminalFile(f)
}

// notLoggedInHint is the one-line remedy every command prints when it needs
// a token and has none.
const notLoggedInHint = "not logged in; run `tf login <url>` or set THINKINGFACE_API_KEY"

// maskToken shows enough of a token to recognise it and no more.
func maskToken(tok string) string {
	if tok == "" {
		return ""
	}
	if len(tok) <= 8 {
		return "****"
	}
	return tok[:3] + "…" + tok[len(tok)-4:]
}

// describeHubError turns a hub error into a one-line, lower-case message
// suitable for "tf: <message>". endpoint is used to build the `tf login`
// hint on 401; ns (may be "") names the namespace to mention on 403.
func describeHubError(err error, endpoint, ns string) string {
	var herr *hub.Error
	if errors.As(err, &herr) {
		switch herr.Status {
		case http.StatusUnauthorized:
			return fmt.Sprintf("authentication failed; run `tf login %s`", endpoint)
		case http.StatusForbidden:
			if ns != "" {
				return fmt.Sprintf("you do not have write access to %s", ns)
			}
			return "you do not have write access to this repository"
		}
		if herr.Message != "" {
			return herr.Message
		}
	}
	return err.Error()
}

// formatSize renders a byte count the way `tf up` progress does: 1000-based,
// one decimal place from kB up ("1.2 kB", "123.4 MB", "2.0 GB").
func formatSize(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"kB", "MB", "GB", "TB", "PB", "EB"}
	v := float64(n)
	i := -1
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// shortOID truncates a commit sha to the conventional 7 hex characters.
func shortOID(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}
