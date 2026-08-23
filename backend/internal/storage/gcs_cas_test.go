package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

// The CAS path cannot be exercised against a hand-written fake — the whole
// point is that the *store* arbitrates. These tests therefore run against
// fake-gcs-server and are skipped unless TF_TEST_GCS_EMULATOR names one
// (docker compose's gcs service, reachable on localhost:4443).
//
// fake-gcs-server 1.55.0+ is required: older builds ignore generation
// preconditions on the resumable upload path (fsouza/fake-gcs-server#2260).
//
// From the host machine:  TF_TEST_GCS_EMULATOR=localhost:4443 go test ./internal/storage/
// From inside the compose network (also exercises object reads, see
// bodyIfReadable):  TF_TEST_GCS_EMULATOR=gcs:4443 go test ./internal/storage/
func casTestStorage(t *testing.T) *GCS {
	t.Helper()
	host := os.Getenv("TF_TEST_GCS_EMULATOR")
	if host == "" {
		t.Skip("TF_TEST_GCS_EMULATOR not set; skipping GCS conditional-write tests")
	}
	ctx := context.Background()
	g, err := NewGCS(ctx, GCSOptions{
		Bucket: "thinkingface-cas-test",
		// A per-test prefix keeps runs (and parallel tests) from colliding on
		// the same object without needing a cleanup step.
		Prefix:       fmt.Sprintf("t%d-%s", time.Now().UnixNano(), strings.ReplaceAll(t.Name(), "/", "-")),
		EmulatorHost: host,
	})
	if err != nil {
		t.Fatalf("NewGCS(emulator): %v", err)
	}
	return g
}

// bodyIfReadable fetches an object body, or reports ok=false when the emulator
// will not serve object downloads to this client.
//
// fake-gcs-server only answers the path-style download URL the GCS client uses
// when the request Host matches its -public-host (gcs:4443 in docker compose).
// Reached as localhost:4443 from the host machine it answers 404 for every
// object, even ones Stat and List can see. That is a property of how the
// emulator is addressed, not of the code under test, so the conditional-write
// assertions below are written to stand without reading bodies, and only the
// content checks are skipped.
func bodyIfReadable(t *testing.T, g *GCS, key string) (string, bool) {
	t.Helper()
	rc, _, err := g.GetWithGeneration(context.Background(), key)
	if errors.Is(err, ErrNotFound) {
		if _, statErr := g.Stat(context.Background(), key); statErr == nil {
			t.Log("emulator refuses object downloads at this host name; skipping body check")
			return "", false
		}
		t.Fatalf("object %s missing", key)
	}
	if err != nil {
		t.Fatalf("GetWithGeneration(%s): %v", key, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return string(body), true
}

func generationOf(t *testing.T, g *GCS, key string) int64 {
	t.Helper()
	info, err := g.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("Stat(%s): %v", key, err)
	}
	return info.Generation
}

func TestGCS_GetWithGeneration_MissingObject(t *testing.T) {
	g := casTestStorage(t)
	_, _, err := g.GetWithGeneration(context.Background(), "absent.json")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestGCS_GetWithGeneration_ReturnsTheSameGenerationAsStat(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()
	if err := g.Put(ctx, "obj.json", strings.NewReader("v1"), "application/json"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, gen, err := g.GetWithGeneration(ctx, "obj.json")
	if errors.Is(err, ErrNotFound) {
		t.Skip("emulator refuses object downloads at this host name")
	}
	if err != nil {
		t.Fatalf("GetWithGeneration: %v", err)
	}
	defer rc.Close()
	if want := generationOf(t, g, "obj.json"); gen != want {
		t.Errorf("generation = %d, want %d", gen, want)
	}
}

func TestGCS_PutIfGeneration_ZeroCreatesOnlyOnce(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()

	if _, err := g.PutIfGeneration(ctx, "index.json", 0, strings.NewReader("first"), "application/json"); err != nil {
		t.Fatalf("create: %v", err)
	}
	gen := generationOf(t, g, "index.json")

	// The second creator must lose: this is how two first-pushes to the same
	// repository are arbitrated.
	_, err := g.PutIfGeneration(ctx, "index.json", 0, strings.NewReader("second"), "application/json")
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("second create error = %v, want ErrPreconditionFailed", err)
	}
	if got := generationOf(t, g, "index.json"); got != gen {
		t.Errorf("generation moved from %d to %d on a rejected write", gen, got)
	}
	if body, ok := bodyIfReadable(t, g, "index.json"); ok && body != "first" {
		t.Errorf("body = %q, want %q", body, "first")
	}
}

func TestGCS_PutIfGeneration_MatchingGenerationSucceedsAndAdvances(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()

	if _, err := g.PutIfGeneration(ctx, "index.json", 0, strings.NewReader("v1"), "application/json"); err != nil {
		t.Fatalf("create: %v", err)
	}
	gen1 := generationOf(t, g, "index.json")
	if gen1 == 0 {
		t.Fatal("generation 0 after create")
	}

	if _, err := g.PutIfGeneration(ctx, "index.json", gen1, strings.NewReader("v2"), "application/json"); err != nil {
		t.Fatalf("update: %v", err)
	}
	gen2 := generationOf(t, g, "index.json")
	if gen2 == gen1 {
		t.Error("generation did not change after a successful write")
	}
	if body, ok := bodyIfReadable(t, g, "index.json"); ok && body != "v2" {
		t.Errorf("body = %q, want v2", body)
	}
}

func TestGCS_PutIfGeneration_StaleGenerationIsRejected(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()

	if _, err := g.PutIfGeneration(ctx, "index.json", 0, strings.NewReader("v1"), "application/json"); err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := generationOf(t, g, "index.json")
	if _, err := g.PutIfGeneration(ctx, "index.json", stale, strings.NewReader("v2"), "application/json"); err != nil {
		t.Fatalf("update: %v", err)
	}
	fresh := generationOf(t, g, "index.json")

	_, err := g.PutIfGeneration(ctx, "index.json", stale, strings.NewReader("v3"), "application/json")
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("stale write error = %v, want ErrPreconditionFailed", err)
	}
	if got := generationOf(t, g, "index.json"); got != fresh {
		t.Errorf("generation = %d, want %d: the stale write must not have landed", got, fresh)
	}
	if body, ok := bodyIfReadable(t, g, "index.json"); ok && body != "v2" {
		t.Errorf("body = %q, want v2", body)
	}
}

func TestGCS_PutIfGeneration_NonZeroGenerationOnMissingObjectIsRejected(t *testing.T) {
	g := casTestStorage(t)
	// Whoever holds a generation for an object that no longer exists is
	// working from a deleted index; the write must not resurrect it silently.
	_, err := g.PutIfGeneration(context.Background(), "index.json", 42, strings.NewReader("x"), "application/json")
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("error = %v, want ErrPreconditionFailed", err)
	}
	if _, statErr := g.Stat(context.Background(), "index.json"); !errors.Is(statErr, ErrNotFound) {
		t.Errorf("Stat after rejected write = %v, want ErrNotFound", statErr)
	}
}

func TestGCS_PutIfGeneration_ExactlyOneWriterWinsFromAGeneration(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()
	if _, err := g.PutIfGeneration(ctx, "index.json", 0, strings.NewReader("v0"), "application/json"); err != nil {
		t.Fatalf("create: %v", err)
	}
	gen := generationOf(t, g, "index.json")

	// This is the linearisation property the whole WAL rests on: many writers
	// holding the same generation, exactly one write landing.
	const writers = 8
	var wg sync.WaitGroup
	results := make(chan error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := g.PutIfGeneration(ctx, "index.json", gen, strings.NewReader(fmt.Sprintf("w%d", i)), "application/json")
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	won, lost := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrPreconditionFailed):
			lost++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || lost != writers-1 {
		t.Errorf("%d winners and %d losers, want exactly 1 and %d", won, lost, writers-1)
	}
}

func TestGCS_PutIfGeneration_ConcurrentReadModifyWriteLosesNoUpdates(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()
	if _, err := g.PutIfGeneration(ctx, "counter", 0, strings.NewReader("0"), "text/plain"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := bodyIfReadable(t, g, "counter"); !ok {
		t.Skip("emulator refuses object downloads at this host name; read-modify-write cannot be exercised")
	}

	// Every worker runs the read-modify-CAS loop UpdateIndex runs. A lost
	// update would show up as a final count below the number of increments.
	const workers, perWorker = 8, 5
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := increment(ctx, g); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("worker: %v", err)
	}

	body, _ := bodyIfReadable(t, g, "counter")
	if body != fmt.Sprint(workers*perWorker) {
		t.Errorf("counter = %s, want %d: updates were lost", body, workers*perWorker)
	}
}

func increment(ctx context.Context, g *GCS) error {
	for attempt := 0; attempt < 500; attempt++ {
		rc, gen, err := g.GetWithGeneration(ctx, "counter")
		if err != nil {
			return err
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		var n int
		if _, err := fmt.Sscanf(string(body), "%d", &n); err != nil {
			return err
		}
		_, err = g.PutIfGeneration(ctx, "counter", gen, strings.NewReader(fmt.Sprint(n+1)), "text/plain")
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrPreconditionFailed) {
			return err
		}
	}
	return errors.New("increment: too many CAS retries")
}

// codedError exercises the structural HTTPCode() branch of
// isPreconditionFailed: gax's apierror wrappers expose the HTTP status this
// way on some call paths, without being a *googleapi.Error. No emulator needed.
type codedError struct{ code int }

func (e *codedError) Error() string { return "coded error" }
func (e *codedError) HTTPCode() int { return e.code }

func TestIsPreconditionFailed_ClassifiesByType(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"googleapi 412", &googleapi.Error{Code: 412}, true},
		{"googleapi 404", &googleapi.Error{Code: 404}, false},
		{"HTTPCode 412", &codedError{code: 412}, true},
		{"HTTPCode 500", &codedError{code: 500}, false},
		{"wrapped HTTPCode 412", fmt.Errorf("write index: %w", &codedError{code: 412}), true},
		{"plain error mentioning 412", errors.New("got 412 Precondition Failed"), false},
	}
	for _, tc := range cases {
		if got := isPreconditionFailed(tc.err); got != tc.want {
			t.Errorf("%s: isPreconditionFailed = %v, want %v", tc.name, got, tc.want)
		}
	}
}
