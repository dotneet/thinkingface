// Git over SSH. The smart HTTP transport in gitserver.go frames every
// exchange as a stateless RPC; over SSH the client instead gets one long-lived
// pair of streams straight into `git upload-pack` / `git receive-pack`. Both
// transports run the same binaries against the same bare repository, so the
// only thing this file adds is the plumbing.

package gitserver

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Streams are the three standard streams of an SSH session, wired to the git
// service process.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// ServeSSH runs one git service against a repository over an SSH session.
//
// gitProtocol is the value of the client's GIT_PROTOCOL environment request
// ("version=2" for a modern client), or "" when it sent none. It is passed
// through verbatim rather than forced: telling upload-pack that the client
// asked for v2 when it did not makes the exchange unparseable to that client.
//
// storagePath comes from the repository row, never from the SSH command line;
// the caller resolves the client's path to a repository first, so nothing the
// client typed reaches the argument list.
func (h *Handler) ServeSSH(ctx context.Context, storagePath string, service Service, gitProtocol string, s Streams) error {
	dir := h.git.Dir(storagePath)

	// serviceArgs is shared with the HTTP transport, and the WAL hook is why:
	// without the core.hooksPath it adds for receive-pack, an SSH push would
	// silently bypass the WAL that an HTTP push records into.
	//
	// No --stateless-rpc here: over SSH the service owns the connection for
	// its whole lifetime.
	args, env := h.serviceArgs(service, storagePath, sshEnv(gitProtocol))
	args = append(args, dir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open %s stdin: %w", service, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open %s stdout: %w", service, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open %s stderr: %w", service, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", service, err)
	}

	// The client holds its side of the channel open until the service is
	// done, so this copy can only finish when the process exits and the pipe
	// breaks. Detached from the wait below for exactly that reason.
	go func() {
		_, _ = io.Copy(stdin, s.In)
		_ = stdin.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// git writes progress and error text here; the client shows it to the
		// user, so it must reach them rather than the server log.
		_, _ = io.Copy(s.Err, stderr)
	}()

	_, copyErr := io.Copy(s.Out, stdout)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s failed: %w", service, err)
	}
	if copyErr != nil {
		return fmt.Errorf("relay %s output: %w", service, copyErr)
	}
	return nil
}

// sshEnv is gitEnv with the protocol value named the way SSH delivers it: an
// optional environment request from the client, where HTTP uses the
// Git-Protocol header. Both are the client's own choice and neither is
// invented for it -- see gitEnv.
func sshEnv(gitProtocol string) []string {
	return gitEnv(gitProtocol)
}

// sanitizeGitProtocol accepts only the shape git actually sends. The value
// arrives from the client -- an SSH environment request, or the Git-Protocol
// header over HTTP -- and it is the one piece of client input that reaches the
// service process's environment, so it is allow-listed rather than filtered:
// `version=<digits>` optionally followed by colon-separated bare words (the
// extension syntax of protocol v2).
func sanitizeGitProtocol(raw string) string {
	if raw == "" || len(raw) > 64 {
		return ""
	}
	for _, part := range strings.Split(raw, ":") {
		if part == "" {
			return ""
		}
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '=', r == '-', r == '_', r == '.':
			default:
				return ""
			}
		}
	}
	if !strings.HasPrefix(raw, "version=") {
		return ""
	}
	return raw
}
