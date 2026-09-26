package tfcli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const logoutUsage = `usage: tf logout [ENDPOINT] [flags]

Forget the saved credentials for a server. ENDPOINT defaults to the endpoint
environment variables (TF_ENDPOINT / THINKINGFACE_ENDPOINT / HF_ENDPOINT) and
then to the configured default endpoint. If the saved token was minted by
'tf login' (not pasted with --token), it is also revoked on the server on a
best-effort basis.

Flags:
  --endpoint URL         same as passing ENDPOINT
  --json                 print {"endpoint", "revoked", "revoke_error"} on stdout
  --verbose              print how the endpoint was resolved to stderr
`

// logoutJSON is the shape written by `tf logout --json`.
type logoutJSON struct {
	Endpoint string `json:"endpoint"`
	// Revoked reports that the saved token was also revoked on the server
	// (only a token tf login minted is; a pasted one never is).
	Revoked     bool   `json:"revoked"`
	RevokeError string `json:"revoke_error,omitempty"`
}

func runLogout(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)
	var jsonOut bool
	fs.BoolVar(&jsonOut, "json", false, "print the result as JSON")

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, logoutUsage)
		return exitOK
	}
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprint(stderr, logoutUsage)
		return exitUsage
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, "tf: logout takes at most one argument (ENDPOINT)")
		fmt.Fprint(stderr, logoutUsage)
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
	// the config file's default: an endpoint set in the environment is what
	// every other command talks to, so it is also the one logout forgets.
	normalized, err := resolveEndpoint(flagEndpoint, file, cf.verbose, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}

	cred, ok := file.Get(normalized)
	if !ok {
		fmt.Fprintf(stderr, "tf: not logged in to %s\n", normalized)
		return exitError
	}

	result := logoutJSON{Endpoint: normalized}
	if cred.TokenID != 0 {
		client := hub.New(normalized, cred.Token, hub.WithUserAgent(userAgent()))
		if err := client.RevokeToken(context.Background(), cred.TokenID); err != nil {
			fmt.Fprintf(stderr, "tf: warning: could not revoke token: %s\n", err)
			result.RevokeError = err.Error()
		} else {
			result.Revoked = true
		}
	}

	file.Remove(normalized)
	if err := file.Save(); err != nil {
		fmt.Fprintf(stderr, "tf: saving config: %s\n", err)
		return exitError
	}

	if jsonOut {
		return writeJSONLine(stdout, stderr, &result)
	}
	fmt.Fprintf(stdout, "Logged out of %s\n", normalized)
	return exitOK
}
