package tfcli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const whoamiUsage = `usage: tf whoami [flags]

Show the identity behind the current token: name, email, token scope, and the
namespaces you can push to (yourself plus any organization where you hold
admin or write).
`

func runWhoami(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, whoamiUsage)
		return exitOK
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, whoamiUsage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "tf: whoami takes no arguments")
		fmt.Fprint(stderr, whoamiUsage)
		return exitUsage
	}

	resolved, err := resolveCreds(cf, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}
	if resolved.Token == "" {
		fmt.Fprintln(stderr, "tf: "+notLoggedInHint)
		return exitError
	}

	client := hub.New(resolved.Endpoint, resolved.Token, hub.WithUserAgent(userAgent()))
	user, err := client.Whoami(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", describeHubError(err, resolved.Endpoint, ""))
		return exitError
	}

	fmt.Fprintf(stdout, "%s (%s) <%s>\n", user.Name, user.Fullname, user.Email)
	fmt.Fprintf(stdout, "token scope: %s\n", user.Role)
	fmt.Fprintf(stdout, "endpoint: %s\n", resolved.Endpoint)
	for _, org := range user.Orgs {
		fmt.Fprintf(stdout, "  %s (%s)\n", org.Name, org.RoleInOrg)
	}

	pushable := []string{user.Name}
	for _, org := range user.Orgs {
		if org.RoleInOrg == "admin" || org.RoleInOrg == "write" {
			pushable = append(pushable, org.Name)
		}
	}
	fmt.Fprintf(stdout, "You can push to: %s\n", strings.Join(pushable, ", "))
	return exitOK
}
