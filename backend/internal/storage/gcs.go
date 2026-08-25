package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type GCS struct {
	client *storage.Client
	bucket string
	prefix string
	// signing is false against the emulator, which cannot verify V4 signatures.
	signing bool
}

type GCSOptions struct {
	Bucket string
	Prefix string
	// EmulatorHost, when set, points the client at fake-gcs-server and disables
	// signed URLs. The GCS client library reads STORAGE_EMULATOR_HOST itself,
	// but we set it explicitly so behaviour does not depend on process env.
	EmulatorHost string
}

func NewGCS(ctx context.Context, opts GCSOptions) (*GCS, error) {
	var clientOpts []option.ClientOption
	signing := true

	if opts.EmulatorHost != "" {
		host := opts.EmulatorHost
		if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
			host = "http://" + host
		}
		// The library keys off this env var for the JSON/XML endpoints it builds.
		_ = os.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://"))
		clientOpts = append(clientOpts,
			option.WithEndpoint(host+"/storage/v1/"),
			option.WithoutAuthentication(),
			option.WithHTTPClient(&http.Client{Timeout: 0}),
		)
		signing = false
	}

	client, err := storage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create gcs client: %w", err)
	}

	g := &GCS{client: client, bucket: opts.Bucket, prefix: strings.Trim(opts.Prefix, "/"), signing: signing}

	if opts.EmulatorHost != "" {
		// fake-gcs-server starts empty; create the bucket so first boot works.
		if err := client.Bucket(opts.Bucket).Create(ctx, "thinkingface-local", nil); err != nil &&
			!strings.Contains(err.Error(), "409") && !strings.Contains(err.Error(), "Conflict") {
			// A pre-existing bucket is fine; anything else is worth surfacing.
			if _, statErr := client.Bucket(opts.Bucket).Attrs(ctx); statErr != nil {
				return nil, fmt.Errorf("ensure emulator bucket %q: %w", opts.Bucket, err)
			}
		}
	}
	return g, nil
}

func (g *GCS) full(key string) string {
	key = strings.TrimPrefix(key, "/")
	if g.prefix == "" {
		return key
	}
	return g.prefix + "/" + key
}

func (g *GCS) obj(key string) *storage.ObjectHandle {
	return g.client.Bucket(g.bucket).Object(g.full(key))
}

func (g *GCS) SupportsSignedURL() bool { return g.signing }

func (g *GCS) PublicURI(key string) string {
	return "gs://" + g.bucket + "/" + g.full(key)
}

func (g *GCS) SignedGetURL(ctx context.Context, key string, ttl time.Duration, downloadName string) (string, error) {
	if !g.signing {
		return "", errors.New("signed URLs unavailable in emulator mode")
	}
	opts := &storage.SignedURLOptions{
		Method:  http.MethodGet,
		Expires: time.Now().Add(ttl),
		Scheme:  storage.SigningSchemeV4,
	}
	if downloadName != "" {
		opts.QueryParameters = map[string][]string{
			"response-content-disposition": {fmt.Sprintf("attachment; filename=%q", downloadName)},
		}
	}
	// Uses the ambient service account via IAM signBlob when no private key is
	// configured, so deployments need no key file.
	return g.client.Bucket(g.bucket).SignedURL(g.full(key), opts)
}

// SignedPutURL signs a PUT for one key. No x-goog-content-length-range is
// signed in: huggingface_hub sends the object bytes without replaying the
// batch action headers, so requiring one would make every HF upload fail the
// signature check. The staged object's size is validated on the server side at
// verify time instead.
func (g *GCS) SignedPutURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if !g.signing {
		return "", errors.New("signed URLs unavailable in emulator mode")
	}
	return g.client.Bucket(g.bucket).SignedURL(g.full(key), &storage.SignedURLOptions{
		Method:  http.MethodPut,
		Expires: time.Now().Add(ttl),
		Scheme:  storage.SigningSchemeV4,
	})
}

// Put writes the whole of r under key. A failure part-way through the transfer
// must leave *no* object behind: keys like blobs/{sha} are trusted by readers
// to hold exactly the content their name claims, and a truncated object there
// would be indistinguishable from a good one and never repaired (PublishBlob
// skips a key that already exists).
//
// That is why the writer gets its own cancellable context. storage.Writer.Close
// is not an abort — it is "finalise what has been written so far", and on a
// writer that was never opened (the reader failed before the first flush) it
// opens one inside Close and commits an empty object. Cancelling the context
// first is the documented way to abort: the upload goroutine's request is bound
// to that context, and Writer.openWriter short-circuits on ctx.Err(), so
// neither the truncated body nor an empty stand-in is ever committed.
// CloseWithError is not a substitute — it is deprecated in favour of exactly
// this, and it returns nil without doing anything when the writer is unopened.
func (g *GCS) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	// Cancelled on every return; after a successful Close the upload has
	// already been observed complete, so the late cancel is a no-op.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := g.obj(key).NewWriter(wctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	if _, err := io.Copy(w, r); err != nil {
		cancel()
		_ = w.Close()
		return fmt.Errorf("write %s: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close %s: %w", key, err)
	}
	return nil
}

func (g *GCS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := g.obj(key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return r, nil
}

func (g *GCS) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	r, err := g.obj(key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", key, err)
	}
	return r, r.Attrs.Generation, nil
}

func (g *GCS) PutIfGeneration(ctx context.Context, key string, generation int64, r io.Reader, contentType string) (int64, error) {
	// generation 0 is "must not exist yet"; anything else is an exact match.
	// Both map onto a single ifGenerationMatch-style precondition, so a lost
	// race and a lost create are indistinguishable to the caller — which is
	// what the WAL wants (continuity-design §11).
	conds := storage.Conditions{GenerationMatch: generation}
	if generation == 0 {
		conds = storage.Conditions{DoesNotExist: true}
	}

	// Same abort-by-cancellation contract as Put: a transfer that dies part-way
	// must not consume the generation it was writing against, which is exactly
	// what finalising a truncated object would do.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := g.obj(key).If(conds).NewWriter(wctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	if _, err := io.Copy(w, r); err != nil {
		cancel()
		_ = w.Close()
		if isPreconditionFailed(err) {
			return 0, ErrPreconditionFailed
		}
		return 0, fmt.Errorf("write %s: %w", key, err)
	}
	if err := w.Close(); err != nil {
		if isPreconditionFailed(err) {
			return 0, ErrPreconditionFailed
		}
		return 0, fmt.Errorf("close %s: %w", key, err)
	}
	// Attrs is documented as valid once Close has returned nil; its
	// Generation is the version this very write created.
	return w.Attrs().Generation, nil
}

// isPreconditionFailed classifies a failed conditional write by error *type*,
// never by matching "412" in a message: the emulator and real GCS word their
// bodies differently, and a substring match would also fire on unrelated text.
//
// The JSON API surfaces *googleapi.Error. Wrappers that only expose the
// generic HTTPCode() accessor (gax apierror, used on some code paths) are
// matched structurally so this file does not have to import them.
func isPreconditionFailed(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code == http.StatusPreconditionFailed
	}
	var coded interface{ HTTPCode() int }
	if errors.As(err, &coded) {
		return coded.HTTPCode() == http.StatusPreconditionFailed
	}
	return false
}

func (g *GCS) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	r, err := g.obj(key).NewRangeReader(ctx, offset, length)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("range read %s: %w", key, err)
	}
	return r, nil
}

func (g *GCS) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	attrs, err := g.obj(key).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat %s: %w", key, err)
	}
	return ObjectInfo{Key: key, Size: attrs.Size, ContentType: attrs.ContentType, Updated: attrs.Updated, Generation: attrs.Generation}, nil
}

func (g *GCS) Copy(ctx context.Context, srcKey, dstKey string) error {
	src := g.obj(srcKey)
	dst := g.obj(dstKey)
	if _, err := dst.CopierFrom(src).Run(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("copy %s -> %s: %w", srcKey, dstKey, err)
	}
	return nil
}

func (g *GCS) Delete(ctx context.Context, key string) error {
	err := g.obj(key).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

func (g *GCS) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	it := g.client.Bucket(g.bucket).Objects(ctx, &storage.Query{Prefix: g.full(prefix)})
	var out []ObjectInfo
	stripe := len(g.prefix)
	if stripe > 0 {
		stripe++ // also drop the separator
	}
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		key := attrs.Name
		if stripe > 0 && len(key) >= stripe {
			key = key[stripe:]
		}
		out = append(out, ObjectInfo{Key: key, Size: attrs.Size, ContentType: attrs.ContentType, Updated: attrs.Updated, Generation: attrs.Generation})
	}
	return out, nil
}
