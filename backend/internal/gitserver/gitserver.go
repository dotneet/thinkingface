// Package gitserver implements the git smart HTTP transport by wrapping the
// git binary's stateless-rpc mode. Reimplementing pack negotiation in Go would
// be slower and less correct than delegating to git itself.
package gitserver

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

type Handler struct {
	git *gitrepo.Manager

	// hooksPath, when non-empty, is passed to receive-pack as core.hooksPath
	// so the image-baked pre-receive hook (docs/continuity-design.md §6.2)
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

// AdvertiseRefs answers GET /info/refs?service=…, the first half of the
// negotiation.
func (h *Handler) AdvertiseRefs(w http.ResponseWriter, storagePath string, service Service) error {
	dir := h.git.Dir(storagePath)

	cmd := exec.Command("git", string(service)[len("git-"):], "--stateless-rpc", "--advertise-refs", dir)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s --advertise-refs: %w: %s", service, err, stderr.String())
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// The advertisement is prefixed with a service pkt-line and a flush packet.
	if _, err := w.Write(pktLine("# service=" + string(service) + "\n")); err != nil {
		return err
	}
	if _, err := w.Write([]byte("0000")); err != nil {
		return err
	}
	_, err := w.Write(stdout.Bytes())
	return err
}

// Serve runs the second half: the client's request body is piped into the
// service and its output streamed straight back.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, storagePath string, service Service) error {
	dir := h.git.Dir(storagePath)

	body := io.Reader(r.Body)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return fmt.Errorf("decompress request: %w", err)
		}
		defer gz.Close()
		body = gz
	}

	args := []string{}
	env := gitEnv()
	if service == ReceivePack && h.hooksPath != "" {
		// -c rather than repository config: the hook is part of the image,
		// never part of the repository's mutable state.
		args = append(args, "-c", "core.hooksPath="+h.hooksPath)
		if h.hookEnv != nil {
			env = append(env, h.hookEnv(storagePath)...)
		}
	}
	args = append(args, string(service)[len("git-"):], "--stateless-rpc", dir)
	cmd := exec.CommandContext(r.Context(), "git", args...)
	cmd.Env = env
	cmd.Stdin = body

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-result", service))
	w.Header().Set("Cache-Control", "no-cache")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open %s stdout: %w", service, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", service, err)
	}

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
		return fmt.Errorf("%s failed: %w: %s", service, err, stderr.String())
	}
	return nil
}

// gitEnv keeps the child process away from the ambient user configuration so
// server behaviour does not depend on whoever built the image.
func gitEnv() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/tmp",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		// Advertise the object format explicitly so clients do not negotiate
		// something the repositories were not created with.
		"GIT_PROTOCOL=version=2",
	}
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
