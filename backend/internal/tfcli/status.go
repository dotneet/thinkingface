package tfcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const statusUsage = `usage: tf status [flags]

Show where tf would connect and as whom: the resolved endpoint and token
(and where each came from: flag, environment or config file), whether the
server accepts the token, the identity behind it, and the namespaces you can
push to. Exit code 0 when logged in, 1 otherwise (so scripts can test it).

Flags:
  --json                 print the status as one JSON object on stdout
  --endpoint URL         server URL
  --token TOKEN          API token (or THINKINGFACE_API_KEY in the environment)
  --api-key KEY          alias of --token
  --verbose              print credential resolution to stderr
`

// statusJSON is the shape written by `tf status --json`.
type statusJSON struct {
	Endpoint       string           `json:"endpoint"`
	EndpointSource string           `json:"endpoint_source"`
	TokenSource    string           `json:"token_source"`
	Token          string           `json:"token"` // masked
	LoggedIn       bool             `json:"logged_in"`
	Error          string           `json:"error,omitempty"`
	User           *statusUserJSON  `json:"user,omitempty"`
	PushTo         []string         `json:"push_to"`
	ConfigPath     string           `json:"config_path"`
	SavedEndpoints []statusSavedCfg `json:"saved_endpoints"`
}

type statusUserJSON struct {
	Name     string          `json:"name"`
	Fullname string          `json:"fullname"`
	Email    string          `json:"email"`
	Scope    string          `json:"scope"`
	Orgs     []statusOrgJSON `json:"orgs"`
}

type statusOrgJSON struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type statusSavedCfg struct {
	Endpoint string `json:"endpoint"`
	Username string `json:"username,omitempty"`
	Default  bool   `json:"default"`
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)
	var jsonOut bool
	fs.BoolVar(&jsonOut, "json", false, "print the status as JSON")

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, statusUsage)
		return exitOK
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, statusUsage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "tf: status takes no arguments")
		fmt.Fprint(stderr, statusUsage)
		return exitUsage
	}

	st := statusJSON{PushTo: []string{}, SavedEndpoints: []statusSavedCfg{}}
	if p, err := config.Path(); err == nil {
		st.ConfigPath = p
	}
	file, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}
	for ep, cred := range file.Credentials {
		st.SavedEndpoints = append(st.SavedEndpoints, statusSavedCfg{
			Endpoint: ep, Username: cred.Username, Default: ep == file.DefaultEndpoint,
		})
	}
	sort.Slice(st.SavedEndpoints, func(i, j int) bool { return st.SavedEndpoints[i].Endpoint < st.SavedEndpoints[j].Endpoint })

	resolved, err := config.Resolve(cf.endpoint, cf.token, os.Getenv, file)
	switch {
	case errors.Is(err, config.ErrNoEndpoint):
		st.Error = err.Error()
	case err != nil:
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	default:
		st.Endpoint, st.EndpointSource = resolved.Endpoint, resolved.EndpointSource
		st.TokenSource, st.Token = resolved.TokenSource, maskToken(resolved.Token)
		if cf.verbose {
			fmt.Fprintf(stderr, "tf: endpoint %s (from %s)\n", resolved.Endpoint, resolved.EndpointSource)
		}
		if resolved.Token == "" {
			st.Error = notLoggedInHint
		} else {
			client := hub.New(resolved.Endpoint, resolved.Token, hub.WithUserAgent(userAgent()))
			user, werr := client.Whoami(context.Background())
			if werr != nil {
				st.Error = describeHubError(werr, resolved.Endpoint, "")
			} else {
				st.LoggedIn = true
				st.User = &statusUserJSON{Name: user.Name, Fullname: user.Fullname, Email: user.Email, Scope: user.Role, Orgs: []statusOrgJSON{}}
				st.PushTo = append(st.PushTo, user.Name)
				for _, org := range user.Orgs {
					st.User.Orgs = append(st.User.Orgs, statusOrgJSON{Name: org.Name, Role: org.RoleInOrg})
					if org.RoleInOrg == "admin" || org.RoleInOrg == "write" {
						st.PushTo = append(st.PushTo, org.Name)
					}
				}
			}
		}
	}

	if jsonOut {
		if err := json.NewEncoder(stdout).Encode(&st); err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
	} else {
		printStatus(stdout, &st)
	}
	if !st.LoggedIn {
		return exitError
	}
	return exitOK
}

// printStatus renders the human-readable status block.
func printStatus(w io.Writer, st *statusJSON) {
	row := func(label, value string) { fmt.Fprintf(w, "%-11s %s\n", label+":", value) }
	if st.Endpoint == "" {
		row("endpoint", "(none)")
	} else {
		row("endpoint", fmt.Sprintf("%s (from %s)", st.Endpoint, st.EndpointSource))
	}
	if st.Token == "" {
		row("token", "(none)")
	} else {
		row("token", fmt.Sprintf("%s (from %s)", st.Token, st.TokenSource))
	}
	if st.LoggedIn {
		row("logged in", "yes")
		u := st.User
		row("user", fmt.Sprintf("%s (%s) <%s>", u.Name, u.Fullname, u.Email))
		row("scope", u.Scope)
		if len(u.Orgs) > 0 {
			parts := make([]string, 0, len(u.Orgs))
			for _, o := range u.Orgs {
				parts = append(parts, o.Name+" ("+o.Role+")")
			}
			row("orgs", strings.Join(parts, ", "))
		}
		row("push to", strings.Join(st.PushTo, ", "))
	} else {
		row("logged in", "no — "+st.Error)
	}
	if st.ConfigPath != "" {
		saved := make([]string, 0, len(st.SavedEndpoints))
		for _, s := range st.SavedEndpoints {
			entry := s.Endpoint
			if s.Username != "" {
				entry += " as " + s.Username
			}
			if s.Default {
				entry += " (default)"
			}
			saved = append(saved, entry)
		}
		if len(saved) == 0 {
			row("config", st.ConfigPath+" (no saved logins)")
		} else {
			row("config", st.ConfigPath+" — "+strings.Join(saved, "; "))
		}
	}
}
