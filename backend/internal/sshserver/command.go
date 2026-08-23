package sshserver

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/gitserver"
)

// ErrBadCommand wraps every rejection ParseCommand can produce. The SSH
// session relays the message to the client, so the wrapped text is written
// for a person staring at a failed `git clone`.
var ErrBadCommand = errors.New("sshserver: unsupported command")

// segmentRe is the shape a namespace or repository name may take. It is the
// same expression the REST API validates new names against
// (api.nameRe) -- deliberately, because anything outside it could never name
// an existing repository, and because it is the guarantee that no path
// segment reaching the git service can contain a slash, a quote, a shell
// metacharacter, or a "..".
var segmentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// Request is a git request extracted from an SSH exec command, after the
// command has been recognised and its path validated. Nothing here is raw
// client text: Namespace and Name have passed segmentRe, and Kind is one of
// two constants.
type Request struct {
	Service   gitserver.Service
	Kind      string // "model" or "dataset"
	Namespace string
	Name      string
}

func (r Request) FullName() string { return r.Namespace + "/" + r.Name }

// ParseCommand recognises the two git services an SSH session may run and
// resolves the repository they name.
//
// Accepted forms, matching what `git clone ssh://host/ns/name` and
// `git clone host:ns/name` send:
//
//	git-upload-pack '<path>'
//	git-receive-pack "<path>"
//	git upload-pack <path>
//
// and, for <path>, `ns/name`, `models/ns/name` or `datasets/ns/name`, with an
// optional leading slash and an optional `.git` suffix. Everything else --
// interactive shells, scp, git-upload-archive, extra arguments, traversal --
// is refused. This is an allow-list, not a filter: the parse fails closed.
func ParseCommand(raw string) (Request, error) {
	cmd := strings.TrimSpace(raw)
	if cmd == "" {
		return Request{}, fmt.Errorf("%w: this server offers git over SSH only, not an interactive shell", ErrBadCommand)
	}

	service, rest, err := splitService(cmd)
	if err != nil {
		return Request{}, err
	}
	path, err := unquoteArg(strings.TrimSpace(rest))
	if err != nil {
		return Request{}, err
	}
	kind, ns, name, err := parseRepoPath(path)
	if err != nil {
		return Request{}, err
	}
	return Request{Service: service, Kind: kind, Namespace: ns, Name: name}, nil
}

// splitService peels the service name off the front of the command. Both the
// hyphenated form (what git actually sends) and the sub-command form some
// tools use are accepted.
func splitService(cmd string) (gitserver.Service, string, error) {
	for _, prefix := range []struct {
		text    string
		service gitserver.Service
	}{
		{"git-upload-pack ", gitserver.UploadPack},
		{"git-receive-pack ", gitserver.ReceivePack},
		{"git upload-pack ", gitserver.UploadPack},
		{"git receive-pack ", gitserver.ReceivePack},
	} {
		if rest, ok := strings.CutPrefix(cmd, prefix.text); ok {
			return prefix.service, rest, nil
		}
	}
	return "", "", fmt.Errorf("%w: only git-upload-pack and git-receive-pack may be run over SSH", ErrBadCommand)
}

// unquoteArg reads the single path argument. git quotes it with single
// quotes; the shell escape for an embedded quote ('\”) is understood so a
// legitimate command never fails here, even though a path containing a quote
// could never survive parseRepoPath anyway.
func unquoteArg(arg string) (string, error) {
	switch {
	case arg == "":
		return "", fmt.Errorf("%w: no repository was named", ErrBadCommand)
	case strings.HasPrefix(arg, "'"):
		if len(arg) < 2 || !strings.HasSuffix(arg, "'") {
			return "", fmt.Errorf("%w: the repository argument is not properly quoted", ErrBadCommand)
		}
		return strings.ReplaceAll(arg[1:len(arg)-1], `'\''`, `'`), nil
	case strings.HasPrefix(arg, `"`):
		if len(arg) < 2 || !strings.HasSuffix(arg, `"`) {
			return "", fmt.Errorf("%w: the repository argument is not properly quoted", ErrBadCommand)
		}
		return arg[1 : len(arg)-1], nil
	case strings.ContainsAny(arg, " \t"):
		// More than one argument. Nothing legitimate sends one, and letting
		// extras through would mean handing them to the service process.
		return "", fmt.Errorf("%w: expected exactly one repository argument", ErrBadCommand)
	default:
		return arg, nil
	}
}

// parseRepoPath maps the client's path onto (kind, namespace, name).
//
// Every segment is matched against segmentRe, which is what makes traversal
// ("../../etc"), absolute escapes and shell metacharacters impossible: a "."
// or ".." segment does not match, because the expression requires a leading
// letter or digit, and no accepted character can end a path segment or start
// a new argument.
func parseRepoPath(path string) (kind, ns, name string, err error) {
	trimmed := strings.Trim(path, "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if trimmed == "" {
		return "", "", "", fmt.Errorf("%w: no repository was named", ErrBadCommand)
	}

	parts := strings.Split(trimmed, "/")
	kind = "model"
	if len(parts) == 3 {
		switch parts[0] {
		case "datasets", "dataset":
			kind = "dataset"
		case "models", "model":
			kind = "model"
		default:
			return "", "", "", fmt.Errorf("%w: %q is not a repository path; use ns/name, models/ns/name or datasets/ns/name",
				ErrBadCommand, path)
		}
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("%w: %q is not a repository path; use ns/name, models/ns/name or datasets/ns/name",
			ErrBadCommand, path)
	}
	for _, seg := range parts {
		if !segmentRe.MatchString(seg) {
			return "", "", "", fmt.Errorf("%w: %q is not a repository path; use ns/name, models/ns/name or datasets/ns/name",
				ErrBadCommand, path)
		}
	}
	return kind, parts[0], parts[1], nil
}
