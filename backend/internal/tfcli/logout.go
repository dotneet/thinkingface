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

Forget the saved credentials for a server (default: the configured default
endpoint). If the saved token was minted by 'tf login' (not pasted with
--token), it is also revoked on the server on a best-effort basis.
`

func runLogout(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)

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

	endpoint := cf.endpoint
	if len(positionals) == 1 {
		endpoint = positionals[0]
	}
	if endpoint == "" {
		endpoint = file.DefaultEndpoint
	}
	if endpoint == "" {
		fmt.Fprintln(stderr, "tf: no endpoint given and no default configured")
		return exitError
	}

	normalized, err := config.NormalizeEndpoint(endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}

	cred, ok := file.Get(normalized)
	if !ok {
		fmt.Fprintf(stderr, "tf: not logged in to %s\n", normalized)
		return exitError
	}

	if cred.TokenID != 0 {
		client := hub.New(normalized, cred.Token, hub.WithUserAgent(userAgent()))
		if err := client.RevokeToken(context.Background(), cred.TokenID); err != nil {
			fmt.Fprintf(stderr, "tf: warning: could not revoke token: %s\n", err)
		}
	}

	file.Remove(normalized)
	if err := file.Save(); err != nil {
		fmt.Fprintf(stderr, "tf: saving config: %s\n", err)
		return exitError
	}

	fmt.Fprintf(stdout, "Logged out of %s\n", normalized)
	return exitOK
}
