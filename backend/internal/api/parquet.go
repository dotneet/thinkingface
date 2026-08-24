package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/repocard"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

const maxParquetRows = 500

// objectKeyFor resolves a repository file to a key in object storage and the
// size of the bytes there. Both layers are content-addressed: an LFS object
// is at its oid's key (behind the ownership gate), a plain git blob at its
// sha's. The syncer publishes blobs on every push; one from a revision it has
// not reached yet is published here on first use, so the viewer works on any
// revision.
func (s *Server) objectKeyFor(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, rev, filePath string) (string, int64, error) {
	entry, _, err := gitRepo.Stat(rev, filePath)
	if err != nil {
		return "", 0, err
	}
	if entry.IsDir {
		return "", 0, gitrepo.ErrPathNotFound
	}
	if entry.LFS != nil {
		key, err := s.ownedLFSKey(ctx, repo, entry.LFS.OID)
		return key, entry.LFS.Size, err
	}
	key, err := gitRepo.PublishBlob(ctx, s.storage, entry.Hash)
	return key, entry.Size, err
}

// parquetTarget is what both viewer endpoints need to know about the file a
// request names: where its bytes live, and enough of the repository to read
// the README at the same revision for feature hints the parquet's own
// metadata left blank.
type parquetTarget struct {
	key      string
	filePath string
	size     int64
	gitRepo  *gitrepo.Repo
	rev      string
}

// resolveParquet is the shared prologue of both viewer endpoints. It has
// already written the error response when ok is false.
func (s *Server) resolveParquet(w http.ResponseWriter, r *http.Request) (pt parquetTarget, ok bool) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return parquetTarget{}, false
	}
	filePath := wildcardPath(r)
	if !strings.HasSuffix(strings.ToLower(filePath), ".parquet") {
		badRequest(w, filePath+" is not a parquet file")
		return parquetTarget{}, false
	}
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return parquetTarget{}, false
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return parquetTarget{}, false
	}
	key, size, err := s.objectKeyFor(r.Context(), repo, gitRepo, rev, filePath)
	if err != nil {
		handleStoreError(w, "locate parquet file", err)
		return parquetTarget{}, false
	}
	return parquetTarget{key: key, filePath: filePath, size: size, gitRepo: gitRepo, rev: rev}, true
}

// completeFeaturesFromReadme fills in Feature for any column the viewer left
// blank (no `datasets`-written key-value metadata), using the repository
// README at the same rev. The README is optional: a missing or unparsable
// one, or one without a `dataset_info.features` block, leaves cols
// unchanged.
func completeFeaturesFromReadme(gitRepo *gitrepo.Repo, rev string, cols []apitypes.ParquetColumn) []apitypes.ParquetColumn {
	readme, err := gitRepo.ReadFile(rev, "README.md", maxReadmeBytes)
	if err != nil {
		return cols
	}
	feats := repocard.Parse(readme).DatasetFeatures()
	return applyReadmeFeatures(cols, feats)
}

// applyReadmeFeatures returns cols with Feature filled in from feats for
// every column whose own Feature is "". It never overwrites a non-empty
// Feature: the parquet file's own key-value metadata always wins over the
// README. Kept as a pure function so it can be unit tested without a running
// server.
func applyReadmeFeatures(cols []apitypes.ParquetColumn, feats map[string]string) []apitypes.ParquetColumn {
	if len(feats) == 0 {
		return cols
	}
	out := make([]apitypes.ParquetColumn, len(cols))
	for i, c := range cols {
		if c.Feature == "" {
			if f, ok := feats[c.Name]; ok {
				c.Feature = f
			}
		}
		out[i] = c
	}
	return out
}

func (s *Server) handleParquetSchema(w http.ResponseWriter, r *http.Request) {
	pt, ok := s.resolveParquet(w, r)
	if !ok {
		return
	}
	schema, err := s.viewer.Schema(r.Context(), pt.key)
	if err != nil {
		handleStoreError(w, "read parquet schema", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ParquetSchemaResponse{
		Path:         pt.filePath,
		Size:         pt.size,
		NumRows:      schema.NumRows,
		NumRowGroups: schema.NumRowGroups,
		Compression:  schema.Compression,
		Columns:      completeFeaturesFromReadme(pt.gitRepo, pt.rev, schema.Columns),
	})
}

func (s *Server) handleParquetRows(w http.ResponseWriter, r *http.Request) {
	pt, ok := s.resolveParquet(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > maxParquetRows {
		limit = maxParquetRows
	}
	var columns []string
	if raw := q.Get("columns"); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			if c = strings.TrimSpace(c); c != "" {
				columns = append(columns, c)
			}
		}
	}

	rows, err := s.viewer.Rows(r.Context(), pt.key, offset, limit, columns)
	if err != nil {
		handleStoreError(w, "read parquet rows", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ParquetRowsResponse{
		Path:    pt.filePath,
		Offset:  rows.Offset,
		Limit:   limit,
		NumRows: rows.NumRows,
		Columns: completeFeaturesFromReadme(pt.gitRepo, pt.rev, rows.Columns),
		Rows:    rows.Rows,
	})
}

var _ = store.ErrNotFound
