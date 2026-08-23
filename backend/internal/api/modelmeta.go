package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/modelmeta"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// handleModelMeta reports the structure of a checkpoint -- the tensor list,
// dtypes, parameter counts and embedded metadata -- by reading only the
// file's header. Weights are never downloaded, so this answers in about the
// time a small file takes, even for a repository holding hundreds of
// gigabytes.
func (s *Server) handleModelMeta(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	filePath := wildcardPath(r)
	format := modelmeta.FormatFor(filePath)
	if format == "" {
		badRequest(w, filePath+" is not a safetensors or PyTorch checkpoint")
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	entry, _, err := gitRepo.Stat(chi.URLParam(r, "rev"), filePath)
	if err != nil {
		handleStoreError(w, "stat file", err)
		return
	}
	if entry.IsDir {
		badRequest(w, filePath+" is a directory")
		return
	}
	// A checkpoint header is read out of the bucket by oid, so the pointer
	// has to be one this repository links -- the same gate every other read
	// of LFS bytes passes through.
	if entry.LFS != nil && !s.lfsObjectOwned(w, r, repo, entry.LFS.OID) {
		return
	}

	cacheKey, size, fetch := s.checkpointSource(gitRepo, entry)
	info, err := s.models.Inspect(r.Context(), cacheKey, format, size, fetch)
	if err != nil {
		// A file whose header does not parse is the user's problem to see,
		// not a server fault: report it as an unreadable file.
		if errors.Is(err, storage.ErrNotFound) {
			notFound(w, "object for "+filePath+" is missing from storage")
			return
		}
		if errors.Is(err, errCheckpointSource) {
			// The bytes could not be fetched. That is a server-side fault, and
			// its text names buckets and object keys, so it stays in the log.
			internalError(w, "read checkpoint", err)
			return
		}
		badRequest(w, "could not read "+filePath+": "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, apitypes.ModelMetaResponse{Path: filePath, Size: size, ModelInfo: *info})
}

// errCheckpointSource marks a failure to fetch a checkpoint's bytes, as
// opposed to a header the reader understood but could not parse. Only the
// latter is safe to describe to the caller.
var errCheckpointSource = errors.New("read checkpoint bytes")

func sourceError(err error) error {
	// A missing or short object describes the file, not the server, so those
	// keep travelling unwrapped and end up as a 404 or a parse complaint.
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	return fmt.Errorf("%w: %w", errCheckpointSource, err)
}

// checkpointSource returns the cache key and a ranged reader for a file. LFS
// objects are read straight out of the bucket; a plain git blob is streamed
// from the object database.
func (s *Server) checkpointSource(gitRepo *gitrepo.Repo, entry gitrepo.Entry) (string, int64, modelmeta.Fetcher) {
	if entry.LFS != nil {
		oid := entry.LFS.OID
		return "lfs:" + oid, entry.LFS.Size, func(ctx context.Context, off, n int64) ([]byte, error) {
			rc, err := s.storage.GetRange(ctx, storage.LFSKey(oid), off, n)
			if err != nil {
				return nil, sourceError(err)
			}
			defer rc.Close()
			data, err := io.ReadAll(io.LimitReader(rc, n))
			if err != nil {
				return nil, sourceError(err)
			}
			return data, nil
		}
	}

	hash := entry.Hash
	return "blob:" + hash.String(), entry.Size, func(_ context.Context, off, n int64) ([]byte, error) {
		// go-git blob readers are sequential, so a ranged read means
		// re-opening and discarding the prefix. Blobs kept in git are small
		// -- anything checkpoint-sized goes to LFS -- so this stays cheap.
		rc, _, err := gitRepo.BlobReader(hash)
		if err != nil {
			return nil, sourceError(err)
		}
		defer rc.Close()
		if off > 0 {
			if _, err := io.CopyN(io.Discard, rc, off); err != nil {
				return nil, sourceError(err)
			}
		}
		data, err := io.ReadAll(io.LimitReader(rc, n))
		if err != nil {
			return nil, sourceError(err)
		}
		return data, nil
	}
}
