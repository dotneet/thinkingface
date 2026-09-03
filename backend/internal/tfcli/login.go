package tfcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const loginUsage = `usage: tf login [ENDPOINT] [flags]

Log in to a thinkingface server and save a token to the config file.

ENDPOINT defaults to the endpoint environment variables (TF_ENDPOINT /
THINKINGFACE_ENDPOINT / HF_ENDPOINT) and then to the configured default
endpoint; if there is none and stdin is a terminal, tf prompts for it.

With --token, the given token is verified (whoami) and saved as-is. Without
--token, tf signs in with a username and password and mints a new write-scoped
personal access token.

Flags:
  --endpoint URL         same as passing ENDPOINT
  --token TOKEN          token to save; "-" reads it from stdin
  --username USER        username for password login
  --password-stdin       read the password from stdin (one line) instead of
                          prompting with echo disabled
  --name NAME            name for the minted token (default tf-cli@<hostname>)
  --verbose              print credential resolution to stderr
`

func runLogin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)
	var username, tokenName string
	var passwordStdin bool
	fs.StringVar(&username, "username", "", "username for password login")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "read password from stdin")
	fs.StringVar(&tokenName, "name", "", "name for the minted token")

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, loginUsage)
		return exitOK
	}
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprint(stderr, loginUsage)
		return exitUsage
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, "tf: login takes at most one argument (ENDPOINT)")
		fmt.Fprint(stderr, loginUsage)
		return exitUsage
	}

	file, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}

	flagEndpoint := cf.endpoint
	if len(positionals) == 1 {
		flagEndpoint = positionals[0]
	}
	// ENDPOINT / --endpoint, then the endpoint environment variables, then
	// the config file's default -- the same order every other command uses.
	// Prompting is the last resort, not the first alternative to a flag.
	endpoint, rerr := resolveEndpoint(flagEndpoint, file, cf.verbose, stderr)
	switch {
	case rerr == nil:
	case errors.Is(rerr, config.ErrNoEndpoint):
		if !isTerminalReader(stdin) {
			fmt.Fprintln(stderr, "tf: no endpoint given; pass ENDPOINT, --endpoint, set THINKINGFACE_ENDPOINT, or run interactively")
			fmt.Fprint(stderr, loginUsage)
			return exitUsage
		}
		fmt.Fprint(stderr, "Endpoint URL: ")
		line, lerr := readLine(stdin)
		if lerr != nil {
			fmt.Fprintf(stderr, "tf: reading endpoint: %s\n", lerr)
			return exitError
		}
		endpoint, err = config.NormalizeEndpoint(line)
		if err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
	default:
		fmt.Fprintf(stderr, "tf: %s\n", rerr)
		return exitError
	}

	ctx := context.Background()

	var user *hub.User
	var cred config.Credential

	if cf.token != "" {
		token := cf.token
		if token == "-" {
			line, rerr := readLine(stdin)
			if rerr != nil {
				fmt.Fprintf(stderr, "tf: reading token: %s\n", rerr)
				return exitError
			}
			token = line
		}
		client := hub.New(endpoint, token, hub.WithUserAgent(userAgent()))
		u, werr := client.Whoami(ctx)
		if werr != nil {
			fmt.Fprintf(stderr, "tf: %s\n", describeHubError(werr, endpoint, ""))
			return exitError
		}
		user = u
		cred = config.Credential{
			Endpoint:  endpoint,
			Token:     token,
			TokenID:   0,
			Username:  user.Name,
			CreatedAt: time.Now().UTC(),
		}
	} else {
		if username == "" {
			if !isTerminalReader(stdin) {
				fmt.Fprintln(stderr, "tf: no username given; pass --username or run interactively")
				fmt.Fprint(stderr, loginUsage)
				return exitUsage
			}
			fmt.Fprint(stderr, "Username: ")
			line, rerr := readLine(stdin)
			if rerr != nil {
				fmt.Fprintf(stderr, "tf: reading username: %s\n", rerr)
				return exitError
			}
			username = line
		}
		// Trimmed here rather than inside readLine, which has to hand back
		// passwords and tokens byte for byte. No username has leading or
		// trailing whitespace, and a stray space -- picked up from a paste,
		// or typed at the prompt -- would otherwise come back as "username or
		// password is incorrect" with nothing to point at. Covers the flag
		// too, which reaches this the same way.
		username = strings.TrimSpace(username)

		var password string
		if passwordStdin {
			line, rerr := readLine(stdin)
			if rerr != nil {
				fmt.Fprintf(stderr, "tf: reading password: %s\n", rerr)
				return exitError
			}
			password = line
		} else if !isTerminalReader(stdin) {
			fmt.Fprintln(stderr, "tf: stdin is not a terminal; use --password-stdin")
			fmt.Fprint(stderr, loginUsage)
			return exitUsage
		} else {
			pw, perr := readPassword(stdin, stderr)
			if perr != nil {
				fmt.Fprintf(stderr, "tf: reading password: %s\n", perr)
				return exitError
			}
			password = pw
		}

		name := tokenName
		if name == "" {
			host, herr := os.Hostname()
			if herr != nil || host == "" {
				host = "unknown"
			}
			name = "tf-cli@" + host
		}

		client := hub.New(endpoint, "", hub.WithUserAgent(userAgent()))
		minted, merr := client.MintToken(ctx, username, password, name, "write")
		if merr != nil {
			fmt.Fprintf(stderr, "tf: %s\n", describeHubError(merr, endpoint, ""))
			return exitError
		}

		verifyClient := hub.New(endpoint, minted.Token, hub.WithUserAgent(userAgent()))
		u, werr := verifyClient.Whoami(ctx)
		if werr != nil {
			fmt.Fprintf(stderr, "tf: %s\n", describeHubError(werr, endpoint, ""))
			return exitError
		}
		user = u
		cred = config.Credential{
			Endpoint:  endpoint,
			Token:     minted.Token,
			TokenID:   minted.ID,
			Username:  user.Name,
			CreatedAt: time.Now().UTC(),
		}
	}

	if user.Role == "read" {
		fmt.Fprintln(stderr, "tf: warning: this token has read-only scope; `tf up` needs a write-scoped token")
	}

	file.Set(cred)
	if err := file.Save(); err != nil {
		fmt.Fprintf(stderr, "tf: saving config: %s\n", err)
		return exitError
	}
	path, perr := config.Path()
	if perr != nil {
		path = "(unknown path)"
	}

	fmt.Fprintf(stdout, "Logged in to %s as %s (%s)\n", endpoint, user.Name, user.Role)
	fmt.Fprintf(stdout, "Credentials saved to %s\n", path)
	return exitOK
}
