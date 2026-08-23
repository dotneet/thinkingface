package wal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// packHeaderSize is "PACK" + 4-byte version + 4-byte object count.
const packHeaderSize = 12

// gitEnv keeps the child process away from ambient user configuration, exactly
// as gitserver.gitEnv does. It is duplicated rather than imported because this
// package must stay below the transport layer: internal/wal is called by
// gitserver, never the other way round.
//
// One deliberate difference from gitserver's copy: no GIT_PROTOCOL=version=2.
// That variable shapes the smart-HTTP negotiation gitserver fronts; everything
// this package runs (init, index-pack, pack-objects, update-ref, symbolic-ref,
// repack) is local plumbing with no protocol negotiation at all. Anyone adding
// a variable to either copy should look at the other and decide explicitly.
func gitEnv() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/tmp",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
}

// quarantineVars are the variables receive-pack exports for its pre-receive
// hook so the hook can see the objects the client just pushed. Until the hook
// exits 0 those objects live in a quarantine directory, not in the repository's
// object database (§6); without these variables `pack-objects` and `cat-file`
// look only at the real objects/ directory and report every pushed commit as
// missing.
//
// GIT_QUARANTINE_PATH is informational, but the other two are what actually
// redirect object lookups, so all three are forwarded together.
var quarantineVars = [...]string{
	"GIT_QUARANTINE_PATH",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
}

// quarantineEnv forwards the quarantine variables when this process is itself a
// git hook. gitEnv deliberately shuts the ambient environment out, so this is
// the one exception, and it is a narrow one: in the API server these variables
// are never set, so the returned slice is empty and nothing changes.
//
// Only object *lookup* is affected. Commands that write (index-pack,
// update-ref) are never run from inside a hook by this package — Materialize
// runs before receive-pack starts — which is why forwarding these is safe here
// but would not be inside recreateBare's `git init`.
func quarantineEnv() []string {
	out := make([]string, 0, len(quarantineVars))
	for _, k := range quarantineVars {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// gitCommand runs git against a bare repository. GIT_DIR is set explicitly so
// the command works no matter what the process working directory is.
func gitCommand(ctx context.Context, gitDir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = gitDir
	env := append(gitEnv(), "GIT_DIR="+gitDir)
	cmd.Env = append(env, quarantineEnv()...)
	return cmd
}

// runGit executes a git command and returns stdout, folding stderr into the
// error so failures are diagnosable from logs alone.
func runGit(ctx context.Context, gitDir string, args ...string) (string, error) {
	cmd := gitCommand(ctx, gitDir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// PackObjects streams a pack containing everything reachable from want but not
// from exclude (§6 step a).
//
// --thin is deliberately absent: WAL entries must be self-contained, because a
// materialising instance applies them against a repository built only from
// earlier entries and has no way to fix up missing bases beyond that.
// --delta-base-offset keeps them small; offsets are internal to the pack, so
// this does not break self-containment.
//
// The returned reader must be closed. If pack-objects fails part way through,
// Read returns that failure instead of io.EOF, so a truncated pack can never be
// mistaken for a complete upload.
func PackObjects(ctx context.Context, gitDir string, want []string, exclude []string) (io.ReadCloser, error) {
	var stdin bytes.Buffer
	for _, h := range want {
		if isAbsent(h) {
			continue
		}
		stdin.WriteString(h + "\n")
	}
	if stdin.Len() == 0 {
		return nil, fmt.Errorf("pack-objects: no wanted objects")
	}
	if len(exclude) > 0 {
		// Everything after --not is negated. Emitted only when non-empty: a
		// bare --not with nothing following is a no-op but reads as a mistake.
		stdin.WriteString("--not\n")
		for _, h := range exclude {
			if isAbsent(h) {
				continue
			}
			stdin.WriteString(h + "\n")
		}
	}

	cmd := gitCommand(ctx, gitDir, "pack-objects", "--revs", "--stdout", "--delta-base-offset")
	cmd.Stdin = &stdin
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open pack-objects stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pack-objects: %w", err)
	}
	return &packReader{cmd: cmd, out: stdout, stderr: stderr}, nil
}

type packReader struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr *bytes.Buffer
	waited bool
	err    error
}

func (p *packReader) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err == io.EOF {
		// Surface a non-zero exit as a read error: io.Copy callers would
		// otherwise happily upload a partial pack and call it a success.
		if waitErr := p.wait(); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

func (p *packReader) Close() error {
	_ = p.out.Close()
	if !p.waited && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.wait()
	return nil
}

func (p *packReader) wait() error {
	if p.waited {
		return p.err
	}
	p.waited = true
	if err := p.cmd.Wait(); err != nil {
		p.err = fmt.Errorf("pack-objects: %w: %s", err, strings.TrimSpace(p.stderr.String()))
	}
	return p.err
}

// packObjectCount peeks at the pack header without consuming it, so the body
// can still be streamed afterwards. Zero objects is a legitimate outcome: it
// happens whenever the wanted commits are already reachable from the excluded
// ones (a re-push of what the server already has).
func packObjectCount(br *bufio.Reader) (uint32, error) {
	head, err := br.Peek(packHeaderSize)
	if err != nil {
		return 0, fmt.Errorf("read pack header: %w", err)
	}
	if !bytes.Equal(head[0:4], []byte("PACK")) {
		return 0, fmt.Errorf("not a pack: header %q", head[0:4])
	}
	return binary.BigEndian.Uint32(head[8:12]), nil
}

// IsEmptyPack reports whether a pack carries no objects. Callers use it to skip
// uploading a WAL entry that would contribute nothing.
func IsEmptyPack(b []byte) bool {
	if len(b) < packHeaderSize {
		return false
	}
	return bytes.Equal(b[0:4], []byte("PACK")) && binary.BigEndian.Uint32(b[8:12]) == 0
}

// knownObjects filters hashes down to the ones this repository can actually
// resolve, preserving order and dropping duplicates.
//
// It exists for one reason: the exclude side of a WAL push is built from the
// index refs, and the index can be *ahead* of the local copy (another instance
// pushed, or a shadow-phase divergence). Handing pack-objects a hash it has
// never seen kills it with "bad revision", turning a benign divergence into a
// failed push. Objects we do not have simply cannot be excluded, and including
// them again is only a size cost.
func knownObjects(ctx context.Context, gitDir string, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	var stdin bytes.Buffer
	seen := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		if isAbsent(h) || seen[h] {
			continue
		}
		seen[h] = true
		stdin.WriteString(h + "\n")
	}
	if stdin.Len() == 0 {
		return nil, nil
	}

	// --batch-check exits 0 even when every input is missing; a missing object
	// is reported as "<input> missing" on stdout, which is data, not failure.
	cmd := gitCommand(ctx, gitDir, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	cmd.Stdin = &stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file --batch-check: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	out := make([]string, 0, len(seen))
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || rest == "missing" || name == "" {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// sortedRefValues returns the distinct hashes an index's refs point at, sorted
// so that the pack-objects input — and therefore the resulting pack — is
// reproducible.
func sortedRefValues(refs map[string]string) []string {
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, len(refs))
	for _, hash := range refs {
		if isAbsent(hash) || seen[hash] {
			continue
		}
		seen[hash] = true
		out = append(out, hash)
	}
	sort.Strings(out)
	return out
}
