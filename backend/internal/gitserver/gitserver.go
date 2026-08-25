// Package gitserver implements the git smart HTTP transport by wrapping the
// git binary's stateless-rpc mode. Reimplementing pack negotiation in Go would
// be slower and less correct than delegating to git itself.
package gitserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/gitexec"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

type Handler struct {
	git *gitrepo.Manager

	// hooksPath, when non-empty, is passed to receive-pack as core.hooksPath
	// so the image-baked pre-receive hook (docs/dev/continuity-design.md §6.2)
	// runs on every push. hookEnv supplies the WAL context that hook needs —
	// the repository's storage path and object-store coordinates — as
	// environment variables; it never includes DATABASE_URL (§14: the hook
	// must not touch the database).
	hooksPath string
	hookEnv   func(storagePath string) []string
}

func New(git *gitrepo.Manager) *Handler { return &Handler{git: git} }

// EnableHooks turns on hook wiring for receive-pack. Callers pass the fixed
// image path and an environment builder; both must be set together.
func (h *Handler) EnableHooks(hooksPath string, hookEnv func(storagePath string) []string) {
	h.hooksPath = hooksPath
	h.hookEnv = hookEnv
}

// Service is one of the two smart HTTP services.
type Service string

const (
	UploadPack  Service = "git-upload-pack"
	ReceivePack Service = "git-receive-pack"
)

func ParseService(raw string) (Service, bool) {
	switch Service(raw) {
	case UploadPack:
		return UploadPack, true
	case ReceivePack:
		return ReceivePack, true
	default:
		return "", false
	}
}

// ErrResponseStarted marks a failure that happened after the status line was
// already on the wire. The caller cannot turn such a request into a 500 -- the
// client has its 200 -- so it is worth telling the two cases apart rather than
// letting an error from either one produce a superfluous WriteHeader.
var ErrResponseStarted = errors.New("response already started")

// AdvertiseRefs answers GET /info/refs?service=…, the first half of the
// negotiation.
//
// The advertisement is buffered rather than streamed on purpose: it is small
// (a pkt-line per ref), and holding it back until the command has exited is
// what lets a failure surface as a 500 instead of an empty 200 that a git
// client reads as "this repository has no refs".
func (h *Handler) AdvertiseRefs(ctx context.Context, w http.ResponseWriter, storagePath string, service Service) error {
	dir := h.git.Dir(storagePath)

	// CommandContext, like Serve: a client that hangs up mid-negotiation must
	// not leave upload-pack walking the object database on its behalf.
	cmd := exec.CommandContext(ctx, "git", string(service)[len("git-"):], "--stateless-rpc", "--advertise-refs", dir)
	cmd.Env = gitEnv()
	// And WaitDelay for the same reason Serve sets it: Stdout and Stderr are
	// buffers rather than pipes, so os/exec makes its own pipes and copies on
	// goroutines that Wait then waits for -- and a grandchild holding one of
	// those write ends keeps Wait blocked with nothing left to cancel, since
	// CommandContext only ever kills the child it started. Less likely to
	// happen here than in Serve, because --advertise-refs does not fork
	// pack-objects, but this is the first request of every clone, fetch and
	// push, so it is not a place to leave the possibility open.
	cmd.WaitDelay = serveWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A delay that expired after git itself exited cleanly means the
		// advertisement in the buffer is complete and only a stray pipe
		// holder was outstanding -- the same call Serve makes.
		if !errors.Is(err, exec.ErrWaitDelay) || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
			return fmt.Errorf("%s --advertise-refs: %w: %s", service, err, stderr.String())
		}
	}

	// The advertisement is prefixed with a service pkt-line and a flush packet.
	var body bytes.Buffer
	body.Write(pktLine("# service=" + string(service) + "\n"))
	body.WriteString("0000")
	body.Write(stdout.Bytes())

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(body.Bytes()); err != nil {
		return fmt.Errorf("write %s advertisement: %w: %w", service, ErrResponseStarted, err)
	}
	return nil
}

// serveWaitDelay bounds the two delays in cmd.Wait that have nothing to do
// with how long the service itself takes. The timer only starts once git has
// exited or the request context is done, so it can never cut a large push
// short -- while git is still working there is nothing here for it to bound.
// What it does bound is the tail: a service that exits leaving a grandchild
// holding the stderr pipe open, or a process that ignores the kill after the
// client hung up. Without it either one keeps this handler, its goroutines and
// its connection alive for as long as the other side feels like it, and the
// number of such requests is chosen by whoever is making them. Ten seconds is
// far more than an orderly shutdown ever needs and short enough that a stuck
// request cannot outlive a deployment.
const serveWaitDelay = 10 * time.Second

// stdinDrainGrace is how long the handler waits for the request body to finish
// copying before it concludes the client has stopped sending and forces the
// read to end. A body that is simply being consumed is done the instant git
// has read it, so this is not a delay any healthy request pays -- it is the
// line between "still arriving" and "never arriving", and the only cost of
// drawing it generously is that a client which really did wander off holds one
// goroutine a moment longer.
const stdinDrainGrace = 2 * time.Second

// A gzipped request body is the one place in this transport where the client
// picks the expansion ratio, so it is the one place a size limit belongs.
//
// An absolute cap on the decompressed size is not an option: a push is
// legitimately unbounded, and any number large enough to be safe for the model
// repositories this server exists for would be too large to be worth checking.
// A ratio cap costs nothing to the real traffic, because the real traffic has
// no ratio to speak of -- a packfile is already deflated, so gzipping it again
// gains nothing, and the pkt-line text of an upload-pack request is mostly
// hex object names, which compress about two to one. Deflate itself tops out
// near 1032:1, so this does not close an unbounded hole; it closes the top
// decade of it, where the only bodies that live are the crafted ones.
//
// The floor keeps the check away from small requests entirely: below it any
// ratio is allowed, since 64 MiB of expansion is not worth refusing whatever
// produced it.
const (
	gzipExpansionFloor = 64 << 20
	maxGzipRatio       = 100
)

var errGzipRatio = errors.New("gzip request body expands far beyond any real git request")

// Serve runs the second half: the client's request body is piped into the
// service and its output streamed straight back.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, storagePath string, service Service) error {
	dir := h.git.Dir(storagePath)

	body, closeBody, err := requestBody(r)
	if err != nil {
		return err
	}

	args, env := h.serviceArgs(service, storagePath, gitEnv())
	args = append(args, "--stateless-rpc", dir)
	// CommandContext, like AdvertiseRefs: a client that hangs up mid-transfer
	// must not leave the service running on its behalf.
	cmd := exec.CommandContext(r.Context(), "git", args...)
	cmd.Env = env
	cmd.WaitDelay = serveWaitDelay

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-result", service))
	w.Header().Set("Cache-Control", "no-cache")

	// Until the copying goroutine below is running, nothing else owns the body,
	// so these three failures have to release it themselves.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		closeBody()
		return fmt.Errorf("open %s stdin: %w", service, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeBody()
		return fmt.Errorf("open %s stdout: %w", service, err)
	}
	if err := cmd.Start(); err != nil {
		closeBody()
		return fmt.Errorf("start %s: %w", service, err)
	}

	// The body is copied on its own goroutine, detached from cmd.Wait, for the
	// same reason ServeSSH does it: os/exec would run the identical copy, but
	// it makes Wait block until that copy finishes -- and the copy finishes
	// when the *client* stops sending, not when git is done. A --stateless-rpc
	// service stops reading the moment it has the whole request (upload-pack
	// exits on the first flush packet), so a client that then goes quiet while
	// holding the body open would pin this handler for as long as it liked.
	// WaitDelay cannot rescue that case: it closes the pipe git reads from,
	// which does nothing for a copy that is blocked reading the body.
	//
	// The mirror image is still open, and is not this transport's to close.
	// A client that opens git-receive-pack and then sends nothing leaves git
	// waiting on stdin, so the copy never returns, cmd.Wait is never reached,
	// and neither serveWaitDelay nor the grace below has anything to act on.
	// Nothing here can tell that apart from a push whose first bytes are
	// simply slow, which is why the server carries no ReadTimeout to begin
	// with -- bounding it needs an idle watchdog ("no progress at all for N
	// seconds"), at the server rather than in one handler.
	var stdinErr error
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		_, stdinErr = io.Copy(stdin, body)
		closeBody()
		_ = stdin.Close()
	}()

	// Nothing may read the request body once this handler returns, and the
	// copy above may still be parked in a Read on a client that stopped
	// sending. Expiring the connection's read deadline is what ends such a
	// Read -- net/http closes the body as soon as the handler returns, and
	// that close waits on the very Read it is trying to interrupt, so without
	// this the stall would only move to the connection's own goroutine.
	//
	// Strictly when the copy really is stuck, though. Expiring the deadline on
	// a healthy request breaks the *next* request on a keep-alive connection:
	// net/http starts a background read between requests to notice a client
	// going away, an expired deadline makes that read fail at once, and the
	// connection's context is cancelled -- which the following request then
	// inherits, already cancelled, and fails before it does anything. That is
	// not hypothetical: it turned every second RPC of a clone into a 500.
	// A normal body finishes copying the moment git has it, so the wait below
	// costs nothing except in the case it exists for.
	defer func() {
		select {
		case <-stdinDone:
			return
		case <-time.After(stdinDrainGrace):
		}
		// Not waited on afterwards. On a real connection the expiry ends the
		// parked Read on its own, and blocking here would hand the stall
		// straight back if it ever did not -- which is exactly what a
		// ResponseWriter that is not a connection does: httptest's recorder
		// answers ErrNotSupported, and there is no deadline to expire.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	}()

	// Flush as data arrives: git clients expect progress during long packs.
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = cmd.Wait()
			return fmt.Errorf("read %s output: %w", service, readErr)
		}
	}
	if err := cmd.Wait(); err != nil {
		// ErrWaitDelay means git itself exited cleanly and only its pipes were
		// still open when the delay ran out. The client already has the
		// complete output, so this is not a failed request -- calling it one
		// would tell handleReceivePack to skip the post-push work for a push
		// that actually landed.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return nil
		}
		// Prefer the reason the input stopped when there is one: a body that
		// tripped the expansion guard makes git fail with an "unexpected EOF"
		// that says nothing about why. Non-blocking on purpose -- if the copy
		// is still running then git's own error is the honest answer, and
		// waiting for the copy is the deadlock this goroutine exists to avoid.
		select {
		case <-stdinDone:
			if copyErr := stdinCopyError(stdinErr); copyErr != nil {
				return fmt.Errorf("read %s request body: %w", service, copyErr)
			}
		default:
		}
		return fmt.Errorf("%s failed: %w: %s", service, err, stderr.String())
	}
	return nil
}

// stdinCopyError filters out the ways feeding git's stdin ends when git simply
// finished first. A --stateless-rpc service exits as soon as it has the whole
// request, and Wait closes the parent's end of the pipe on the way out, so a
// copy that was still going gets a closed pipe or an EPIPE. Neither says
// anything went wrong; only a genuine failure to read the body does.
func stdinCopyError(err error) error {
	if err == nil ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, syscall.EPIPE) {
		return nil
	}
	return err
}

// requestBody is the reader git's stdin is fed from. A git client gzips the
// request when it grows (remote-curl does this for a large upload-pack want/
// have list), so Content-Encoding has to be honoured here or the service is
// handed compressed bytes it cannot parse.
//
// The raw body is deliberately not passed through http.MaxBytesReader: the
// bytes of a push are the payload this whole server is for, and a cap on them
// is a cap on how large a model anyone can upload. Only the decompressed path
// is bounded, and only by ratio -- see maxGzipRatio.
//
// The returned close function releases the decompressor and must be called by
// whoever finishes reading, on that goroutine: closing a gzip.Reader that
// another goroutine is still reading is a data race.
func requestBody(r *http.Request) (io.Reader, func(), error) {
	if !strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		return r.Body, func() {}, nil
	}
	counted := &countingReader{r: r.Body}
	gz, err := gzip.NewReader(counted)
	if err != nil {
		return nil, nil, fmt.Errorf("decompress request: %w", err)
	}
	return &ratioLimitedReader{src: gz, compressed: counted}, func() { _ = gz.Close() }, nil
}

// countingReader records how many bytes were actually read from the client.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ratioLimitedReader passes a decompressed stream through untouched until it
// has expanded by more than maxGzipRatio, which no request a git client
// produces ever does.
type ratioLimitedReader struct {
	src        io.Reader
	compressed *countingReader
	out        int64
}

func (l *ratioLimitedReader) Read(p []byte) (int, error) {
	n, err := l.src.Read(p)
	l.out += int64(n)
	// The compressed count runs ahead of the output (gzip.Reader buffers what
	// it has not decoded yet), which can only make the measured ratio smaller
	// than the real one -- the permissive direction, which is the right one to
	// err in for a check that must never refuse a real push.
	if l.out > gzipExpansionFloor && l.out > l.compressed.n*maxGzipRatio {
		return n, fmt.Errorf("%w: %d bytes from %d", errGzipRatio, l.out, l.compressed.n)
	}
	return n, err
}

// serviceArgs builds the `git` arguments up to and including the service name
// ("upload-pack" / "receive-pack"), and the environment to run it with, for
// both transports. baseEnv is the transport's own environment: gitEnv() over
// HTTP, sshEnv(gitProtocol) over SSH. The caller appends what is left --
// `--stateless-rpc` and the directory for HTTP, the directory alone for SSH,
// where the service owns the connection for its whole lifetime.
//
// The receive-pack branch is the reason this is one function. Without
// `-c core.hooksPath` the push runs whatever hooks the repository itself
// carries, which for a bare repository this server created is none: the WAL
// hook never fires and the push is recorded nowhere, while the client is told
// it succeeded. Both transports need it, and a copy per transport is a way
// for SSH to silently bypass what HTTP records.
//
// -c rather than repository config: the hook is part of the image, never part
// of the repository's mutable state.
func (h *Handler) serviceArgs(service Service, storagePath string, baseEnv []string) (args, env []string) {
	args, env = []string{}, baseEnv
	if service == ReceivePack && h.hooksPath != "" {
		args = append(args, "-c", "core.hooksPath="+h.hooksPath)
		if h.hookEnv != nil {
			env = append(env, h.hookEnv(storagePath)...)
		}
	}
	return append(args, string(service)[len("git-"):]), env
}

// gitEnv is the shared git environment (internal/gitexec) plus the protocol
// version this transport speaks. Over HTTP the version is fixed: every request
// is a fresh stateless RPC and this handler always frames it as v2. The SSH
// transport passes the client's own value through instead -- see sshEnv.
func gitEnv() []string {
	return append(gitexec.Env(), "GIT_PROTOCOL=version=2")
}

// pktLine encodes one git protocol packet.
func pktLine(s string) []byte {
	return fmt.Appendf(nil, "%04x%s", len(s)+4, s)
}

// HeadsAfterPush lists the branch tips of a repository, which the API uses to
// work out what a push changed.
func (h *Handler) HeadsAfterPush(storagePath string) (map[string]string, error) {
	repo, err := h.git.Open(storagePath)
	if err != nil {
		return nil, err
	}
	branches, err := repo.Branches()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(branches))
	for _, b := range branches {
		hash, err := repo.RefTarget("refs/heads/" + b)
		if err != nil {
			continue
		}
		out[b] = hash.String()
	}
	return out, nil
}
