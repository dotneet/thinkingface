package wal

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// VerifyReport is the outcome of one repository's reconciliation. Match is the
// answer; the rest exists so an operator can act on a mismatch without
// re-running anything.
type VerifyReport struct {
	Match bool
	// Reason is a one-line summary when Match is false, empty otherwise.
	Reason string
	// RefsMissing are refs the on-disk repository has and the WAL does not.
	RefsMissing []string
	// RefsExtra are refs only the WAL has — usually a deletion that never got
	// mirrored.
	RefsExtra []string
	// RefsDiffer are refs both sides have at different hashes.
	RefsDiffer []string
	// Generation is the index generation actually materialised and compared.
	Generation int64
}

// Verify answers the Phase 3 question of §15: does the WAL reconstruct the
// repository that is on disk?
//
// It materialises the WAL into scratchDir and compares three things against
// pdDir, in increasing order of cost:
//
//  1. the ref sets, which is where a divergence almost always shows up;
//  2. the object set reachable from each ref, which catches a pack that is
//     internally valid but incomplete — refs matching does not prove the
//     history behind them does;
//  3. `git fsck --strict` on the reconstruction, which is git's own verdict on
//     whether the result is a usable repository.
//
// A repository with no index yet is not an error: during the migration most
// repositories are in that state, and reporting it as a failure would bury the
// real divergences. It comes back as Match=false, Reason="no index", which the
// caller can route to Seed.
//
// An error return means the comparison could not be carried out (storage
// unreachable, a pack that will not apply); it is not a verdict.
func Verify(ctx context.Context, st storage.Storage, scratchDir, pdDir, storagePath string) (*VerifyReport, error) {
	// Materialize rebuilds from scratch whenever it cannot reason about a
	// directory, and rebuilding means `rm -rf`. Pointing it at the repository
	// under test would destroy the very thing being verified.
	if sameDir(scratchDir, pdDir) {
		return nil, fmt.Errorf("verify %s: scratch and repository directories must differ (%s)", storagePath, pdDir)
	}

	_, gen, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return nil, err
	}
	if gen == 0 {
		return &VerifyReport{Reason: "no index"}, nil
	}
	if err := Materialize(ctx, st, scratchDir, storagePath); err != nil {
		return nil, fmt.Errorf("verify %s: materialize: %w", storagePath, err)
	}

	report := &VerifyReport{Generation: gen}
	// Report the generation that was actually applied, not the one read a
	// moment ago: a push in between changes what we are comparing, and a report
	// naming the wrong version is worse than none.
	if applied := LocalGeneration(scratchDir); applied != 0 {
		report.Generation = applied
	}

	walRefs, err := listRefs(ctx, scratchDir)
	if err != nil {
		return nil, err
	}
	pdRefs, err := listRefs(ctx, pdDir)
	if err != nil {
		return nil, err
	}

	for ref, hash := range pdRefs {
		switch other, ok := walRefs[ref]; {
		case !ok:
			report.RefsMissing = append(report.RefsMissing, ref)
		case !sameHash(hash, other):
			report.RefsDiffer = append(report.RefsDiffer, ref)
		}
	}
	for ref := range walRefs {
		if _, ok := pdRefs[ref]; !ok {
			report.RefsExtra = append(report.RefsExtra, ref)
		}
	}
	sort.Strings(report.RefsMissing)
	sort.Strings(report.RefsExtra)
	sort.Strings(report.RefsDiffer)

	if n := len(report.RefsMissing) + len(report.RefsExtra) + len(report.RefsDiffer); n > 0 {
		report.Reason = fmt.Sprintf("refs differ (%d missing, %d extra, %d mismatched)",
			len(report.RefsMissing), len(report.RefsExtra), len(report.RefsDiffer))
		return report, nil
	}

	if ref, err := compareReachable(ctx, scratchDir, pdDir, pdRefs); err != nil {
		return nil, err
	} else if ref != "" {
		report.Reason = "objects reachable from " + ref + " differ"
		return report, nil
	}

	if err := fsck(ctx, scratchDir); err != nil {
		report.Reason = "fsck failed: " + err.Error()
		return report, nil
	}

	report.Match = true
	return report, nil
}

// compareReachable returns the first ref whose reachable object set differs, or
// "" when every ref agrees.
//
// Refs are walked in a stable order and results are cached by tip hash: a
// repository with many tags on the same commit would otherwise re-walk the same
// history once per tag. Only hashes are compared, never object contents — the
// packs on both sides were written independently, so identical hashes over the
// identical closure is the strongest statement available, and git's own
// integrity checks cover the bytes.
func compareReachable(ctx context.Context, walDir, pdDir string, refs map[string]string) (string, error) {
	names := make([]string, 0, len(refs))
	for ref := range refs {
		names = append(names, ref)
	}
	sort.Strings(names)

	checked := make(map[string]bool, len(refs))
	for _, ref := range names {
		tip := refs[ref]
		if checked[tip] {
			continue
		}
		checked[tip] = true

		walObjects, err := reachableObjects(ctx, walDir, tip)
		if err != nil {
			return "", err
		}
		pdObjects, err := reachableObjects(ctx, pdDir, tip)
		if err != nil {
			return "", err
		}
		if !sameStringSet(walObjects, pdObjects) {
			return ref, nil
		}
	}
	return "", nil
}

// reachableObjects lists every object id reachable from tip. `rev-list
// --objects` prints "<oid> [path]"; the path is where the object was found and
// says nothing about identity, so only the id is kept.
func reachableObjects(ctx context.Context, gitDir, tip string) (map[string]struct{}, error) {
	out, err := runGit(ctx, gitDir, "rev-list", "--objects", tip)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, line := range strings.Split(out, "\n") {
		oid, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if oid == "" {
			continue
		}
		set[oid] = struct{}{}
	}
	return set, nil
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// fsck asks git whether the materialised repository is sound. --strict turns
// the checks up to what fsck applies on the receiving end of a push, which is
// the standard a WAL reconstruction has to meet: anything it rejects here would
// also poison a clone served from this copy.
func fsck(ctx context.Context, gitDir string) error {
	_, err := runGit(ctx, gitDir, "fsck", "--full", "--strict")
	return err
}

func sameDir(a, b string) bool {
	pa, err := filepath.Abs(a)
	if err != nil {
		pa = a
	}
	pb, err := filepath.Abs(b)
	if err != nil {
		pb = b
	}
	return filepath.Clean(pa) == filepath.Clean(pb)
}
