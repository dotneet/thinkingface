package lfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

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
//  4. server-side copy to storage.LFSKey(oid) of the generation step 1 saw,
//     so what is published is the object those checks inspected rather than
//     whatever a concurrent upload left under the same name -- see
//     publishStaged. GCS rewrites in chunks and the client library loops the
//     rewrite token, so a 10 GiB object promotes without a byte passing
//     through this process;
//  5. only then link the object to the repository. A link recorded before the
//     copy would advertise an object whose bytes are not at the key yet --
//     dedup, downloads and gc all read the link as proof the content exists;
//  6. delete the staging object, best effort. A failure here leaves garbage
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
	// The namespace's allowance, charged where the bytes are actually paid for:
	// the link below is a row in repo_lfs_objects and that is exactly what
	// store.UsageByRepo sums (see quota.go). Checking only in Batch left the two
	// upload routes that call PromoteStagedFrom -- the browser's multipart
	// endpoint and the emulator's transfer proxy -- outside enforcement
	// entirely.
	//
	// Before confirmDigest rather than after: a refusal here should not first
	// pay for a full re-read of the staged object out of the bucket. It is
	// after the size check because info.Size is what the link will record, so
	// this charges the object's real length rather than the one the client
	// declared.
	if err := h.chargeQuota(ctx, repoID, oid, info.Size); err != nil {
		return err
	}
	if proof == digestUnproven {
		if err := h.confirmDigest(ctx, oid, staging, info.Generation); err != nil {
			return err
		}
	}
	if err := h.publishStaged(ctx, oid, staging, info.Generation); err != nil {
		return err
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
// It hashes the generation promoteFrom's Stat saw, not whatever is in staging
// by the time the read starts, so the digest, the size and the copy in
// publishStaged are all statements about one version of the object. A driver
// that cannot pin a generation reads the live object, and publishStaged's
// confirmUnchanged -- which spans this read as well as the copy -- is what
// catches a swap there.
func (h *Handler) confirmDigest(ctx context.Context, oid, staging string, generation int64) error {
	var (
		rc  io.ReadCloser
		err error
	)
	versioned, pinned := h.storage.(storage.Versioned)
	if pinned {
		rc, err = versioned.GetGeneration(ctx, staging, generation)
	} else {
		rc, err = h.storage.Get(ctx, staging)
	}
	if errors.Is(err, storage.ErrNotFound) {
		if pinned {
			// The generation the size check passed is gone: replaced by a
			// later upload to the same key, or swept by gc. Either way there
			// is nothing left that this promotion checked.
			return &StagedObjectChangedError{OID: oid}
		}
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
// It is only the fallback for a driver without storage.Versioned (see
// publishStaged): a re-stat proves the key was unchanged at the instant it
// ran, not at the instant of the copy after it. A driver that does not report
// generations reports zero for every version and this passes, which is the
// honest limit of what such a driver allows.
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

// publishStaged copies the staged object onto its content-addressed key --
// the generation of it that promoteFrom inspected, and only that.
//
// Everything checked before this -- the size, and on the unproven path the
// digest -- describes one version of the staged object, and on the signed-URL
// path nothing stops a second version arriving: storage.LFSStagingKey is
// derived from the repository id and the oid, both of which the client names,
// and the signed upload URL for it can still be live while this runs. A copy
// of "whatever is at the key now" would then publish bytes nobody checked onto
// lfs/{oid}, which is not one promotion's problem: it is a key every
// repository on the instance shares, Batch treats its existence as proof the
// content is there (so every later upload of that oid is deduplicated onto it),
// promoteAlreadyDone trusts it without re-hashing, and nothing rewrites it
// afterwards. One forged object would stand in for the real one everywhere.
//
// So on a driver that can (storage.Versioned; GCS, real or emulator) the copy
// names the generation: a replaced staging object is simply not what gets
// copied, and a superseded generation that is no longer stored fails the copy
// instead of publishing its successor.
//
// A driver without generation addressing falls back to re-statting the key and
// requiring the same generation (confirmUnchanged), which leaves the instant
// between that re-stat and the copy open. Only test doubles take this branch
// today. The paths that choose their own staging key (the proxy and browser
// uploads) stage under a private random key (storage.LFSIncomingKey) that
// nothing else can name, so for them this is a second line of defence either
// way; digestHashedOnIngest vouches for the bytes that streamed through the
// request, not for what is at the key now, which is why the pinning applies
// whatever the proof was.
func (h *Handler) publishStaged(ctx context.Context, oid, staging string, generation int64) error {
	dst := storage.LFSKey(oid)
	if versioned, ok := h.storage.(storage.Versioned); ok {
		err := versioned.CopyGeneration(ctx, staging, generation, dst)
		if errors.Is(err, storage.ErrNotFound) {
			return &StagedObjectChangedError{OID: oid}
		}
		if err != nil {
			return fmt.Errorf("promote staged object: %w", err)
		}
		return nil
	}
	if err := h.confirmUnchanged(ctx, oid, staging, generation); err != nil {
		return err
	}
	if err := h.storage.Copy(ctx, staging, dst); err != nil {
		return fmt.Errorf("promote staged object: %w", err)
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
		// Passed through unchanged so handleLFSVerify can answer 507 with this
		// sentence: it names the namespace, the limit and the shortfall, and
		// folding it into a generic failure would leave `git push` reporting
		// "object could not be verified" for a condition the operator can act
		// on.
		var overQuota *QuotaExceededError
		if errors.As(err, &overQuota) {
			return overQuota
		}
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
