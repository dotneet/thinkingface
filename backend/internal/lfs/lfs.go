// Package lfs implements the Git LFS batch API. Object bytes never pass
// through this process in a real GCS deployment: clients get V4 signed URLs
// and transfer directly with the bucket.
package lfs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ContentType is the media type the LFS spec mandates on batch requests and
// responses.
const ContentType = "application/vnd.git-lfs+json"

// ErrUnsupportedOperation reports a batch naming something other than upload
// or download, which is a malformed request rather than a server fault.
var ErrUnsupportedOperation = errors.New("lfs: unsupported operation")

// oidRe matches the only object id this server accepts. Every oid reaches
// storage.LFSKey, which splices it straight into an object key, so an
// unchecked value from a batch request would let a client name a key outside
// the content-addressed lfs/ prefix.
var oidRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidOID reports whether oid is a lowercase sha256 hex digest.
func ValidOID(oid string) bool { return oidRe.MatchString(oid) }

type BatchRequest struct {
	Operation string      `json:"operation"`
	Transfers []string    `json:"transfers"`
	Ref       *Ref        `json:"ref"`
	Objects   []ObjectRef `json:"objects"`
	HashAlgo  string      `json:"hash_algo"`
}

type Ref struct {
	Name string `json:"name"`
}

type ObjectRef struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

type ObjectResponse struct {
	OID           string            `json:"oid"`
	Size          int64             `json:"size"`
	Authenticated bool              `json:"authenticated"`
	Actions       map[string]Action `json:"actions,omitempty"`
	Error         *ObjectError      `json:"error,omitempty"`
}

type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type BatchResponse struct {
	Transfer string           `json:"transfer"`
	Objects  []ObjectResponse `json:"objects"`
	HashAlgo string           `json:"hash_algo"`
}

// lfsRecorder is the store surface the batch/verify paths need. *store.Store
// implements it; tests substitute a fake so they can drive ErrLFSObjectGone
// without Postgres.
type lfsRecorder interface {
	RecordLFSObject(ctx context.Context, repoID int64, oid string, size int64, confirmPresent func(key string) (bool, error)) error
	// RepoHasLFSObject answers whether the repository is entitled to an
	// object at all. Objects are stored by content hash with no repository
	// in the key, so this is the only thing separating "this repository has
	// this file" from "somebody on this instance uploaded these bytes once".
	RepoHasLFSObject(ctx context.Context, repoID int64, oid string) (bool, error)
}

type Handler struct {
	store   lfsRecorder
	storage storage.Storage
	// ttl is the floor for a signed URL's lifetime; maxTTL is the ceiling a
	// transfer-sized lifetime is clamped to. See TTLFor.
	ttl       time.Duration
	maxTTL    time.Duration
	publicURL string
	secret    []byte
}

// New builds the batch handler. ttl is the base signed-URL lifetime
// (TF_SIGNED_URL_TTL) and maxTTL the ceiling a large transfer may stretch it
// to (TF_SIGNED_URL_MAX_TTL).
func New(st *store.Store, obj storage.Storage, ttl, maxTTL time.Duration, publicURL, secret string) *Handler {
	return &Handler{store: st, storage: obj, ttl: ttl, maxTTL: maxTTL, publicURL: publicURL, secret: []byte(secret)}
}

// minTransferBytesPerSecond is the throughput a signed URL's lifetime is
// budgeted against: 1 MiB/s. It is not a prediction of how fast clients are,
// it is the slowest link we are willing to let a transfer fail on. Pushing a
// 10 GiB dataset over a home uplink of a few MiB/s is an ordinary thing to do
// here, and a URL that dies mid-PUT costs the whole object -- git-lfs restarts
// the transfer from zero -- while a URL that outlives the transfer costs
// nothing but a slightly wider window on a key that only ever accepts one
// specific object. The asymmetry is why this number is pessimistic.
const minTransferBytesPerSecond = 1 << 20

// maxTTLSeconds is the largest whole-second count time.Duration can hold.
const maxTTLSeconds = int64(math.MaxInt64) / int64(time.Second)

// signingLimit is GCS's own ceiling on a V4 signed URL's lifetime: 7 days.
// Asking for more does not produce a long-lived URL, it produces an error at
// signing time -- i.e. a failed push rather than a slow one. It is enforced
// here, not left to the operator, because TF_SIGNED_URL_MAX_TTL is allowed to
// be zero ("no ceiling") and a batch large enough to reach 7 days at the
// assumed floor throughput is roughly 600 GiB, which is not an absurd dataset
// for this hub.
const signingLimit = 7 * 24 * time.Hour

// MaxSignedURLTTL returns the longest lifetime TTLFor can hand out for a given
// configured ceiling: the ceiling itself, or signingLimit when there is none
// (max <= 0) or when the configured one is above what GCS will sign.
//
// It exists so that nothing has to re-derive that answer from the config value
// alone. `thinkingface gc` needs it -- its staging window has to outlast every
// URL still in flight -- and reading TF_SIGNED_URL_MAX_TTL as if it were the
// effective maximum gets the no-ceiling case exactly backwards: zero means
// URLs live *longer* (up to 7 days), not shorter.
func MaxSignedURLTTL(max time.Duration) time.Duration {
	if max <= 0 || max > signingLimit {
		return signingLimit
	}
	return max
}

// TTLFor returns how long a signed URL for a transfer of n bytes must live:
// the base lifetime plus the time n bytes take at minTransferBytesPerSecond,
// clamped to max.
//
// A batch hands out every URL at once but the client uses them one at a time,
// so the last object's URL is first touched after every earlier object has
// finished transferring. Sizing the lifetime off a single object's size (or
// off a fixed hour) is what makes a 100-object push of 1 GiB files fail with
// 403s two thirds of the way through.
//
// max is a hard ceiling, even below base: it is the operator's statement of
// how long a leaked URL stays useful. A max <= 0 means "no ceiling", which
// still leaves signingLimit. n <= 0 (unknown size) gets base.
func TTLFor(base, max time.Duration, n int64) time.Duration {
	ttl := base
	if n > 0 {
		// Seconds are computed in integer bytes first: n * time.Second
		// overflows int64 nanoseconds at about 9.2 GB, which is squarely
		// inside the range this function exists for.
		secs := n / minTransferBytesPerSecond
		if secs >= maxTTLSeconds {
			ttl = time.Duration(math.MaxInt64)
		} else if transfer := time.Duration(secs) * time.Second; base > time.Duration(math.MaxInt64)-transfer {
			ttl = time.Duration(math.MaxInt64)
		} else {
			ttl = base + transfer
		}
	}
	if max > 0 && ttl > max {
		ttl = max
	}
	if ttl > signingLimit {
		ttl = signingLimit
	}
	return ttl
}

var _ lfsRecorder = (*store.Store)(nil)

// proxyHref builds a self-authenticating URL for the emulator transfer path.
// git-lfs and huggingface_hub both assume an upload href is pre-signed and
// send no Authorization header with the transfer itself, so the credential has
// to live in the URL exactly as it does for a real GCS signed URL.
func (h *Handler) proxyHref(op string, repoID int64, oid string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return fmt.Sprintf("%s/api/v1/lfs/%d/%s?op=%s&exp=%d&sig=%s",
		h.publicURL, repoID, url.PathEscape(oid), op, exp, h.sign(op, repoID, oid, exp))
}

func (h *Handler) sign(op string, repoID int64, oid string, exp int64) string {
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%s\n%d\n%s\n%d", op, repoID, oid, exp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyProxySignature reports whether a proxy request carries a signature this
// server issued and that has not expired.
func (h *Handler) VerifyProxySignature(op string, repoID int64, oid, expRaw, sig string) bool {
	if sig == "" || expRaw == "" {
		return false
	}
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h.sign(op, repoID, oid, exp)), []byte(sig)) == 1
}

// Batch answers an LFS batch request for one repository. authToken is echoed
// back to the client in the action headers so its follow-up proxy requests
// authenticate; it is empty in signed-URL mode, where the URL carries its own
// credentials.
func (h *Handler) Batch(ctx context.Context, repoID int64, req *BatchRequest, authToken string) (*BatchResponse, error) {
	resp := &BatchResponse{Transfer: "basic", HashAlgo: "sha256", Objects: make([]ObjectResponse, 0, len(req.Objects))}

	if req.Operation != "upload" && req.Operation != "download" {
		return nil, fmt.Errorf("%w %q", ErrUnsupportedOperation, req.Operation)
	}

	// Actions are decided here but minted below, because their lifetime
	// depends on the size of the *whole* batch: the client transfers these
	// objects one after another over a single connection, so the URL for the
	// last one has to still be valid after every earlier one has gone across.
	var (
		pending    []pendingAction
		totalBytes int64
	)

	for _, obj := range req.Objects {
		item := ObjectResponse{OID: obj.OID, Size: obj.Size, Authenticated: true}

		// A per-object error is how the LFS spec reports a value it will not
		// act on, so one bad entry does not fail the whole batch.
		if !ValidOID(obj.OID) || obj.Size < 0 {
			item.Error = &ObjectError{Code: 422, Message: "oid must be a sha256 hex digest and size must not be negative"}
			resp.Objects = append(resp.Objects, item)
			continue
		}

		switch req.Operation {
		case "upload":
			exists, err := h.stored(ctx, obj.OID, obj.Size)
			if err != nil {
				return nil, err
			}
			if exists {
				// Deduplicated: the client uploads nothing and the commit still
				// references the same content. RecordLFSObject re-checks
				// storage under the row lock; if a concurrent GC already
				// deleted the bytes, ErrLFSObjectGone means we must ask
				// the client to upload rather than treat the oid as present.
				err := h.link(ctx, repoID, obj.OID, obj.Size)
				if err != nil && !errors.Is(err, store.ErrLFSObjectGone) {
					return nil, err
				}
				if err == nil {
					resp.Objects = append(resp.Objects, item)
					continue
				}
			}
			// Only objects that actually get transferred count towards the
			// batch's byte total; a deduplicated hit costs no wall clock.
			pending = append(pending, pendingAction{index: len(resp.Objects), op: "upload", obj: obj})
			totalBytes += obj.Size

		case "download":
			// Membership first, and with the same answer as a genuinely
			// absent object: the caller has already proved it may read *this*
			// repository, but an oid it merely knows about must not become a
			// download URL for bytes that live in a repository it was not
			// given. Every legitimate route into this repository records
			// the link before a commit can reference the object (upload dedup
			// and Verify below, the emulator proxy upload, and the post-push
			// indexer's LinkLFSObjects).
			owned, err := h.store.RepoHasLFSObject(ctx, repoID, obj.OID)
			if err != nil {
				return nil, fmt.Errorf("check lfs object ownership: %w", err)
			}
			exists := false
			if owned {
				if exists, err = h.stored(ctx, obj.OID, obj.Size); err != nil {
					return nil, err
				}
			}
			if !exists {
				item.Error = &ObjectError{Code: 404, Message: "object " + obj.OID + " not found"}
				break
			}
			pending = append(pending, pendingAction{index: len(resp.Objects), op: "download", obj: obj})
			totalBytes += obj.Size
		}

		resp.Objects = append(resp.Objects, item)
	}

	ttl := TTLFor(h.ttl, h.maxTTL, totalBytes)
	for _, p := range pending {
		switch p.op {
		case "upload":
			upload, err := h.uploadAction(ctx, repoID, p.obj, authToken, ttl)
			if err != nil {
				return nil, err
			}
			// The verify call comes after the last byte of the last object,
			// so its signature gets the batch-wide lifetime too.
			verifyExp := time.Now().Add(ttl).Unix()
			resp.Objects[p.index].Actions = map[string]Action{
				"upload": upload,
				"verify": {
					Href: fmt.Sprintf("%s/api/v1/lfs/%d/verify?op=verify&exp=%d&sig=%s",
						h.publicURL, repoID, verifyExp, h.sign("verify", repoID, "", verifyExp)),
					Header: authHeader(authToken),
				},
			}
		case "download":
			download, err := h.downloadAction(ctx, repoID, p.obj, storage.LFSKey(p.obj.OID), authToken, ttl)
			if err != nil {
				return nil, err
			}
			resp.Objects[p.index].Actions = map[string]Action{"download": download}
		}
	}
	return resp, nil
}

// pendingAction is an object the batch decided to hand transfer actions for,
// held back until the batch's total transfer size (and so the URL lifetime) is
// known.
type pendingAction struct {
	index int
	op    string
	obj   ObjectRef
}

// stored reports whether the object's bytes are really in the bucket. The key
// is derived from the oid alone -- lfs/ is the one and only home, immutable
// and shared across repositories -- so this asks the bucket, not the ledger.
//
// It deliberately carries no authorisation: an object being present says
// nothing about who may read it. The download path answers that separately,
// with RepoHasLFSObject, before it ever gets here.
func (h *Handler) stored(ctx context.Context, oid string, size int64) (bool, error) {
	return h.storedAt(ctx, storage.LFSKey(oid), size)
}

// storedAt reports whether key holds exactly size bytes right now. It takes
// the key rather than an oid, so it can be used on a path where the ledger
// cannot answer yet -- Verify runs before the row exists.
//
// The comparison is exact, zero included. It used to be skipped for size <= 0
// ("the client did not tell us"), which handed dedup to anyone able to name an
// oid -- and oids are public, every LFS pointer in every readable repository
// is one. Dedup's whole premise is that declaring the oid *and* its size is
// evidence of holding the content; a request declaring zero asserted only the
// oid, and got the object linked to a repository the caller controls. From
// there RepoHasLFSObject answers yes and the download half of Batch, resolve
// and the transfer proxy all hand over somebody else's bytes. It is the same
// leniency promoteFrom removed on the promotion side, left behind on this one.
//
// A genuinely empty object is unaffected: it declares zero and it is zero, so
// the sizes agree like any other pair and it deduplicates normally.
func (h *Handler) storedAt(ctx context.Context, key string, size int64) (bool, error) {
	info, err := h.storage.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat lfs object: %w", err)
	}
	if info.Size != size {
		return false, nil
	}
	return true, nil
}

// uploadAction signs a write to the *staging* key, never to the
// content-addressed lfs/ key. What arrives over a signed URL is unverified by
// construction -- the bytes never pass through this process, so nothing checks
// their digest until verify -- and lfs/{oid} is immutable, shared by every
// repository on the instance and treated as authoritative by dedup. Letting a
// truncated or mislabelled transfer land there would corrupt the object for
// every repository referencing it. Verify promotes the staged bytes instead.
func (h *Handler) uploadAction(ctx context.Context, repoID int64, obj ObjectRef, authToken string, ttl time.Duration) (Action, error) {
	if h.storage.SupportsSignedURL() {
		url, err := h.storage.SignedPutURL(ctx, storage.LFSStagingKey(repoID, obj.OID), ttl)
		if err != nil {
			return Action{}, fmt.Errorf("sign upload url: %w", err)
		}
		return Action{Href: url, ExpiresIn: int(ttl.Seconds())}, nil
	}
	return Action{
		Href:      h.proxyHref("upload", repoID, obj.OID, ttl),
		Header:    authHeader(authToken),
		ExpiresIn: int(ttl.Seconds()),
	}, nil
}

// downloadAction signs the object's content-addressed key. It takes the key
// rather than deriving it so the caller's authorisation check and the URL it
// hands out are visibly about the same object.
func (h *Handler) downloadAction(ctx context.Context, repoID int64, obj ObjectRef, key, authToken string, ttl time.Duration) (Action, error) {
	if h.storage.SupportsSignedURL() {
		url, err := h.storage.SignedGetURL(ctx, key, ttl, "")
		if err != nil {
			return Action{}, fmt.Errorf("sign download url: %w", err)
		}
		return Action{Href: url, ExpiresIn: int(ttl.Seconds())}, nil
	}
	return Action{
		Href:      h.proxyHref("download", repoID, obj.OID, ttl),
		Header:    authHeader(authToken),
		ExpiresIn: int(ttl.Seconds()),
	}, nil
}

func authHeader(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": token}
}

// ErrNotStaged reports that no bytes were found for an object: neither under
// its staging key nor, already promoted, under its content-addressed key.
var ErrNotStaged = errors.New("lfs: object was not uploaded")

// SizeMismatchError reports staged bytes whose length is not the one the
// client declared in the batch request. The object stays in staging: it is
// never promoted onto the shared content-addressed key.
type SizeMismatchError struct {
	OID  string
	Got  int64
	Want int64
}

func (e *SizeMismatchError) Error() string {
	return fmt.Sprintf("object %s is %d bytes, expected %d", e.OID, e.Got, e.Want)
}

// DigestMismatchError reports staged bytes whose sha256 is not the oid they
// were uploaded under. Like a size mismatch the object stays in staging and is
// never promoted: lfs/{oid} is what every repository on the instance gets when
// it asks for that oid, so bytes that are not the ones the oid names must not
// reach it.
type DigestMismatchError struct {
	OID string
	Got string
}

func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf("object %s hashes to %s; the uploaded bytes are not the ones this oid names", e.OID, e.Got)
}

// StagedObjectChangedError reports that the staged bytes were rewritten while
// the promotion was inspecting them, which makes the size and digest it
// checked statements about bytes that are no longer there. Nothing is
// promoted; the client's own retry re-checks whatever is in staging then.
type StagedObjectChangedError struct{ OID string }

func (e *StagedObjectChangedError) Error() string {
	return fmt.Sprintf("object %s changed while it was being verified; retry the upload", e.OID)
}

// link records the object against the repository, re-confirming under the row
// lock that the bytes are really at the content-addressed key.
func (h *Handler) link(ctx context.Context, repoID int64, oid string, size int64) error {
	return h.store.RecordLFSObject(ctx, repoID, oid, size, func(k string) (bool, error) {
		return h.storedAt(ctx, k, size)
	})
}

// digestProof says whether anything has actually hashed the bytes a promotion
// is about to publish. It is a parameter rather than an assumption because the
// two upload paths differ in exactly this respect, and getting it wrong is the
// difference between a content-addressed store and a client-addressed one.
type digestProof int

const (
	// digestUnproven: nothing in this process has seen these bytes. That is
	// the signed-URL path -- the client PUTs straight to the bucket -- and it
	// is why promotion re-reads the staged object and hashes it.
	digestUnproven digestProof = iota
	// digestHashedOnIngest: the bytes streamed through this process and were
	// hashed on their way to the staging key, so re-reading them from the
	// bucket would buy nothing but latency and egress. Only a caller that
	// hashed the whole body itself may claim this. Both that do -- the
	// emulator proxy upload and the browser upload -- refuse the transfer
	// outright when the digest is not the declared oid (and the browser
	// upload derives the oid from the stream in the first place), so
	// promotion never sees bytes nobody vouched for.
	digestHashedOnIngest
)

// promote turns a staged upload into a real object. The order is the whole
// point:
//
//  1. stat the staging key -- absent means nothing was uploaded;
//  2. check the size before anything is published, so bytes that do not match
//     what the client declared never reach the shared key. It is one metadata
//     call and it rejects a truncated transfer without reading a byte;
//  3. confirm the digest, unless the bytes were already hashed on ingest.
//     This is what makes lfs/{oid} content-addressed rather than
//     client-labelled -- see confirmDigest;
//  4. re-stat the staging key and require the generation step 1 saw, so the
//     copy publishes the object those checks inspected rather than whatever a
//     concurrent upload left under the same name -- see confirmUnchanged;
//  5. server-side Copy to storage.LFSKey(oid). GCS rewrites in chunks and the
//     client library loops the rewrite token, so a 10 GiB object promotes
//     without a byte passing through this process;
//  6. only then link the object to the repository. A link recorded before the
//     copy would advertise an object whose bytes are not at the key yet --
//     dedup, downloads and gc all read the link as proof the content exists;
//  7. delete the staging object, best effort. A failure here leaves garbage
//     under tmp/uploads/ for the collector, which is strictly better than
//     failing a verify whose object is already safely published.
//
// It is idempotent: a retried verify whose staging object is already gone
// succeeds if the promoted object is present at the expected size, because
// that is exactly what a completed promotion looks like.
func (h *Handler) promote(ctx context.Context, repoID int64, oid string, size int64, proof digestProof) error {
	return h.promoteFrom(ctx, repoID, oid, size, storage.LFSStagingKey(repoID, oid), proof)
}

// promoteFrom is promote with the staging key spelled out, for the one caller
// that cannot use storage.LFSStagingKey: the browser upload endpoint hashes
// the bytes as it receives them, so they are already written somewhere by the
// time their oid -- and therefore that key -- exists.
func (h *Handler) promoteFrom(ctx context.Context, repoID int64, oid string, size int64, staging string, proof digestProof) error {
	info, err := h.storage.Stat(ctx, staging)
	if errors.Is(err, storage.ErrNotFound) {
		return h.promoteAlreadyDone(ctx, repoID, oid, size)
	}
	if err != nil {
		return fmt.Errorf("stat staged object: %w", err)
	}
	// The declared size is now checked exactly, including zero. It used to be
	// skipped for size <= 0 ("the client did not tell us"), which let a caller
	// turn the only check this path had off by declaring nothing: PUT any
	// bytes to the staging key, verify with size 0, and they were promoted
	// onto a content-addressed key that every repository shares. Callers that
	// measured the object themselves pass what they measured, and the LFS
	// batch and verify requests both carry a size, so nothing legitimate
	// leaves it unstated. A genuinely empty object still passes: it declares
	// zero and it is zero.
	if info.Size != size {
		return &SizeMismatchError{OID: oid, Got: info.Size, Want: size}
	}
	if proof == digestUnproven {
		if err := h.confirmDigest(ctx, oid, staging); err != nil {
			return err
		}
	}
	// Everything checked so far -- the size, and on the unproven path the
	// digest -- describes the version of the staged object the Stat above
	// returned, and nothing so far has said that version is still there.
	// storage.LFSStagingKey is derived from the repository id and the oid,
	// both of which the client names, and the signed upload URL for it can
	// still be live while this runs, so a second request can replace those
	// bytes between the checks and the copy below. Requiring the generation to
	// be unchanged is what makes the copy publish the object this promotion
	// actually inspected.
	//
	// It runs whatever the proof was. digestHashedOnIngest is a statement
	// about the bytes that streamed through *this* request, not about what is
	// at the staging key now: two uploads of the same length can each hash
	// their own body happily, and whichever promotes would copy whatever the
	// other left there onto lfs/{oid} -- a key every repository on the
	// instance shares, that dedup treats as authoritative, and that nothing
	// rewrites afterwards. The callers that hash on ingest stage under private
	// keys precisely so this cannot arise, which makes this their second line
	// of defence rather than their first.
	if err := h.confirmUnchanged(ctx, oid, staging, info.Generation); err != nil {
		return err
	}

	if err := h.storage.Copy(ctx, staging, storage.LFSKey(oid)); err != nil {
		return fmt.Errorf("promote staged object: %w", err)
	}
	if err := h.link(ctx, repoID, oid, info.Size); err != nil {
		return err
	}
	if err := h.storage.Delete(ctx, staging); err != nil {
		slog.Warn("lfs: staged object left behind after promotion",
			"oid", oid, "repo_id", repoID, "key", staging, "error", err)
	}
	return nil
}

// confirmDigest streams the staged object once and checks that it really
// hashes to the oid it was uploaded under.
//
// This is the only thing standing between a signed-URL upload and the shared
// content-addressed key. Those bytes go straight from the client to the
// bucket, so until this read nothing on the server has seen them: with only a
// size check, anyone holding write access to a single repository could take an
// oid the instance does not have yet, request an upload URL for it, PUT
// whatever they liked, and have it promoted to lfs/{oid}. From then on every
// repository pushing the real object would be deduplicated onto the forgery,
// because Batch reads the bucket as proof the content exists.
//
// The cost is honest and unavoidable: one extra full read of the object out of
// the bucket, and verify does not answer until it finishes. On a 10 GiB model
// that is minutes of wall clock and 10 GiB of reads the previous
// implementation did not pay -- git-lfs's verify request has no timeout of its
// own, but a proxy in front of this server may, so the read budget is the one
// thing to watch when tuning a deployment for very large objects. Nothing is
// buffered: io.Copy streams into the hash in 32 KiB chunks, so memory is flat
// whatever the object's size.
//
// There is deliberately no way to switch this off. An integrity gate with an
// opt-out is the same hole with an extra step, and the paths that can afford
// to skip the read (digestHashedOnIngest) already have a stronger proof.
//
// It does not check by itself that the bytes it hashed are still the ones in
// staging: promoteFrom's confirmUnchanged spans this read as well as the copy
// that follows it, and one comparison across the whole window is both cheaper
// and stronger than one per step.
func (h *Handler) confirmDigest(ctx context.Context, oid, staging string) error {
	rc, err := h.storage.Get(ctx, staging)
	if errors.Is(err, storage.ErrNotFound) {
		// Deleted between the stat above and this read -- gc sweeping an
		// upload it judged abandoned. Nothing was promoted, so this is the
		// same answer as never having uploaded.
		return ErrNotStaged
	}
	if err != nil {
		return fmt.Errorf("read staged object: %w", err)
	}
	defer rc.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, rc); err != nil {
		return fmt.Errorf("hash staged object: %w", err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != oid {
		return &DigestMismatchError{OID: oid, Got: got}
	}
	return nil
}

// confirmUnchanged reports whether the staged object is still the version an
// earlier Stat saw. Generations only move forward, so an unchanged one means
// nothing was written to the key in between: it turns "whatever is at this key
// now" back into "the object this request inspected", which is the only thing
// that makes the checks before it worth anything once a key can have more than
// one writer.
//
// A driver that does not report generations reports zero for every version and
// this passes. That is the honest limit of what can be done here -- closing the
// last instant before the copy would need storage.Copy to take a generation
// precondition, which the interface does not have -- and it is why the staging
// key is the thing to keep private (storage.LFSIncomingKey) wherever a caller
// is free to choose it.
func (h *Handler) confirmUnchanged(ctx context.Context, oid, staging string, generation int64) error {
	after, err := h.storage.Stat(ctx, staging)
	if errors.Is(err, storage.ErrNotFound) {
		// Deleted since the checks above -- gc sweeping an upload it judged
		// abandoned. Nothing was promoted, so this is the same answer as
		// never having uploaded.
		return ErrNotStaged
	}
	if err != nil {
		return fmt.Errorf("re-stat staged object: %w", err)
	}
	if after.Generation != generation {
		return &StagedObjectChangedError{OID: oid}
	}
	return nil
}

// promoteAlreadyDone handles a verify with nothing in staging. Clients retry
// verify (git-lfs does so on any transient failure) and the same object can be
// verified twice through different paths, so "the staging object is gone"
// has to succeed for the case it usually means: this repository's promotion
// already ran.
//
// What it must *not* do is treat the object merely being present at
// storage.LFSKey(oid) as grounds to link it. The staging key carries the
// repository id, so a staged object is proof that this repository uploaded
// these bytes; the content-addressed key carries no repository at all, so its
// existence says nothing about entitlement. Linking on presence would make
// verify a way to claim any object whose oid the caller can name -- POST the
// oid and size, get the link, then commit a pointer and read somebody else's
// bytes back out through your own repository. That is precisely the hole
// RepoHasLFSObject and ownedLFSKey exist to close on every other path
// (resolve.go, commit.go, the download half of Batch), and it was open here
// before staging made the two cases distinguishable.
//
// So the fallback proof is a link this repository already holds. Nothing
// legitimate needs more: a dedup hit is linked by Batch and never reaches
// verify, and a genuine upload always has staging.
//
// It does not re-hash the promoted object. The link it requires can only have
// been written by a promotion that already confirmed the digest, and lfs/{oid}
// is never a signed PUT target, so nothing can have rewritten it since. Making
// every git-lfs verify retry re-read a 10 GiB object would be a large bill for
// no additional proof.
func (h *Handler) promoteAlreadyDone(ctx context.Context, repoID int64, oid string, size int64) error {
	owned, err := h.store.RepoHasLFSObject(ctx, repoID, oid)
	if err != nil {
		return fmt.Errorf("check lfs object ownership: %w", err)
	}
	if !owned {
		return ErrNotStaged
	}
	// Linked already, so this is a retry. Still confirm the bytes are where
	// the link claims: a GC between the first verify and this one would
	// otherwise have this report success on an object that is gone.
	info, err := h.storage.Stat(ctx, storage.LFSKey(oid))
	if errors.Is(err, storage.ErrNotFound) {
		return ErrNotStaged
	}
	if err != nil {
		return fmt.Errorf("stat lfs object: %w", err)
	}
	// Deliberately more permissive about the declared size than promoteFrom
	// is, and it costs nothing: this path publishes nothing and links nothing,
	// so a client that leaves the size out gets an answer about an object it
	// already legitimately holds rather than a failed retry. On promoteFrom
	// the same leniency is what let unverified bytes onto the shared key.
	if size > 0 && info.Size != size {
		return &SizeMismatchError{OID: oid, Got: info.Size, Want: size}
	}
	return nil
}

// PromoteStagedFrom publishes bytes the caller has already written to a
// staging key of its own choosing and links them to the repository. Every
// upload path that hashes the body as it streams through this process uses it
// -- the browser upload endpoint and the emulator's transfer proxy -- so they
// all run one promotion sequence: the size check, the copy onto the
// content-addressed key, the link, the staging cleanup, and their ordering.
//
// The key is the caller's to choose because both of those paths want it to be
// unguessable rather than derived from the request (storage.LFSIncomingKey):
// a staging key two requests can both name is a staging object they can
// overwrite for each other, and a digest is only a statement about the bytes
// that were hashed. promoteFrom refuses to copy a staged object that moved
// since it checked it, but a private key means there is nothing to refuse.
//
// The caller must have hashed the whole body and found it to be oid: that
// promise is what lets promotion skip re-reading the object out of the bucket,
// and it is the caller's half of keeping lfs/{oid} content-addressed. A caller
// that cannot make it has to go through Verify, which hashes for itself.
func (h *Handler) PromoteStagedFrom(ctx context.Context, repoID int64, oid string, size int64, stagingKey string) error {
	if !ValidOID(oid) {
		return errors.New("oid must be a sha256 hex digest")
	}
	if stagingKey == "" {
		return errors.New("lfs: staging key is required")
	}
	return h.promoteFrom(ctx, repoID, oid, size, stagingKey, digestHashedOnIngest)
}

// Verify is the second half of a signed-URL upload: it promotes the staged
// object to its content-addressed key and records it against the repository.
// It is mandatory, not advisory -- RecordLFSObject only ever runs here (or on
// the proxy path), and a commit referencing an LFS file is rejected unless the
// repository holds the link.
//
// Everything it is told comes from the client, and the transfer it is
// attesting to never touched this process, so it takes none of it on trust:
// the staged bytes are hashed before they are published, and the size must be
// the one the object actually has. Size is not the security property here --
// the digest is -- but it is a cheap first cut that fails a truncated upload
// without reading the object.
//
// Storage and database faults are logged rather than described to the client:
// their text carries bucket names and connection detail. A digest mismatch is
// described, because it is the client's own upload being reported back to it,
// and logged, because it is either a corrupt transfer or an attempt to publish
// bytes under somebody else's oid.
func (h *Handler) Verify(ctx context.Context, repoID int64, oid string, size int64) error {
	if !ValidOID(oid) {
		return errors.New("oid must be a sha256 hex digest")
	}
	if size < 0 {
		return fmt.Errorf("object %s: size must not be negative", oid)
	}
	err := h.promote(ctx, repoID, oid, size, digestUnproven)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotStaged), errors.Is(err, store.ErrLFSObjectGone):
		return fmt.Errorf("object %s was not uploaded", oid)
	default:
		var mismatch *SizeMismatchError
		if errors.As(err, &mismatch) {
			return mismatch
		}
		var digest *DigestMismatchError
		if errors.As(err, &digest) {
			slog.Warn("lfs verify: staged object does not hash to its oid",
				"oid", oid, "repo_id", repoID, "actual_oid", digest.Got)
			return digest
		}
		var changed *StagedObjectChangedError
		if errors.As(err, &changed) {
			return changed
		}
		slog.Error("lfs verify: promote object", "oid", oid, "repo_id", repoID, "error", err)
		return fmt.Errorf("object %s could not be verified", oid)
	}
}
