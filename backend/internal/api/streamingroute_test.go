package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// streamingRoute decides on the route the router will dispatch to, not on the
// path. The bypass cases below are all requests whose *decoded path* contains
// one of the strings the old substring checks looked for -- "/resolve/",
// "/commit/", "/info/lfs/", "/info/refs", a trailing "/log" -- in a file name,
// directory, revision, namespace or run name, which is to say in something
// the caller chose. Every one of them used to run without handlerTimeout.
func TestStreamingRoute(t *testing.T) {
	const oid = "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	tests := []struct {
		method, path string
		want         bool
	}{
		// ---- the routes that really stream
		{"GET", "/alice/bert/resolve/main/config.json", true},
		{"HEAD", "/alice/bert/resolve/main/config.json", true},
		{"GET", "/models/alice/bert/resolve/main/model.safetensors", true},
		{"GET", "/datasets/alice/squad/resolve/main/data/train.parquet", true},
		{"GET", "/alice/bert/resolve/refs%2Fpr%2F1/config.json", true},
		{"GET", "/alice/bert.git/info/refs?service=git-upload-pack", true},
		{"POST", "/alice/bert.git/git-upload-pack", true},
		{"POST", "/datasets/alice/squad.git/git-receive-pack", true},
		{"POST", "/models/alice/bert.git/git-receive-pack", true},
		{"POST", "/alice/bert.git/info/lfs/objects/batch", true},
		{"POST", "/datasets/alice/squad.git/info/lfs/objects/verify", true},
		{"POST", "/api/models/alice/bert/commit/main", true},
		{"POST", "/api/datasets/alice/squad/commit/refs%2Fpr%2F1", true},
		{"PUT", "/api/v1/lfs/12/" + oid, true},
		{"GET", "/api/v1/lfs/12/" + oid, true},
		{"POST", "/api/v1/lfs/12/verify", true},
		{"POST", "/api/v1/upload/model/alice/bert/main", true},
		{"POST", "/api/v1/experiments/alice/exp/proj/log", true},
		// A repository may itself be called "resolve"; its downloads stream.
		{"GET", "/resolve/resolve/resolve/main/x.bin", true},

		// ---- bypasses of the old substring checks
		// The checkpoint-header parse the deadline was introduced for.
		{"GET", "/api/v1/model-meta/model/ns/n/main/resolve/evil.safetensors", false},
		{"GET", "/api/v1/parquet/dataset/ns/n/rows/main/commit/x.parquet", false},
		{"GET", "/api/v1/parquet/dataset/ns/n/schema/main/info/lfs/x.parquet", false},
		// A tree listing under a directory named "resolve".
		{"GET", "/api/models/ns/n/tree/main/resolve", false},
		{"GET", "/api/v1/repos/model/ns/n/tree/main/resolve/deep", false},
		// An organisation (or user) named "resolve". The first looks exactly
		// like /{ns}/{name}/resolve/{rev}/* to anything that does not know
		// "/api/" is a static prefix chi prefers.
		{"GET", "/api/models/resolve/main/tree/x", false},
		{"GET", "/api/v1/repos/model/resolve/x", false},
		// An experiment run named "x/log" (sent as %2F, decoded into the path).
		{"PATCH", "/api/v1/experiments/ns/repo/proj/runs/x%2Flog", false},
		{"GET", "/api/v1/experiments/ns/repo/proj/runs/x%2Flog/artifacts", false},
		// git suffixes as file names.
		{"GET", "/api/v1/raw/model/ns/n/main/info/refs", false},
		{"PUT", "/api/v1/edit/model/ns/n/main/git-receive-pack", false},

		// ---- ordinary metadata, and requests no route answers
		{"GET", "/api/models/alice/bert", false},
		{"GET", "/api/models/alice/bert/tree/main", false},
		{"POST", "/api/models/alice/bert/preupload/main", false},
		{"GET", "/api/v1/repos", false},
		{"GET", "/healthz", false},
		{"GET", "/alice/bert/resolve", false},
		// The right path with a method it is not registered for is a 405,
		// answered in microseconds; nothing to exempt.
		{"GET", "/alice/bert.git/git-upload-pack", false},
	}
	routes := defaultRoutes()
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			if got := streamingRoute(routes, r); got != tt.want {
				t.Errorf("streamingRoute(%s %s) = %v, want %v (route %q)", tt.method, tt.path, got, tt.want,
					routes.Find(chi.NewRouteContext(), tt.method, r.URL.EscapedPath()))
			}
		})
	}
}

// The exemption is a closed list of registered routes. Walking the routing
// table pins it: a new route under a streaming prefix, or a renamed streaming
// route, changes this list and has to be looked at rather than silently
// widening -- or silently narrowing -- what runs without a deadline.
func TestStreamingRoute_ExemptSetIsExactlyTheStreamingRoutes(t *testing.T) {
	var got []string
	err := chi.Walk(defaultRoutes(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if streamingPattern(route) {
			got = append(got, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	var want []string
	for _, m := range repoTransportMounts {
		for _, sub := range []string{
			"GET /resolve/{rev}/*", "HEAD /resolve/{rev}/*",
			"GET /info/refs", "POST /git-upload-pack", "POST /git-receive-pack",
			"POST /info/lfs/objects/batch", "POST /info/lfs/objects/verify",
		} {
			method, path, _ := strings.Cut(sub, " ")
			want = append(want, method+" "+m.prefix+path)
		}
	}
	want = append(want,
		"POST /api/{repoType:models|datasets}/{ns}/{name}/commit/{rev}",
		"PUT /api/v1/lfs/{repoID}/{oid}",
		"GET /api/v1/lfs/{repoID}/{oid}",
		"POST /api/v1/lfs/{repoID}/verify",
		"POST /api/v1/upload/{kind}/{ns}/{name}/{rev}",
		"POST /api/v1/experiments/{ns}/{repo}/{project}/log",
	)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("routes exempt from the handler deadline:\n got: %q\nwant: %q", got, want)
	}
}

// Inside a real router, boundHandlerTime asks that router -- the one chi puts
// in the route context -- rather than the package's default table.
func TestBoundHandlerTime_UsesTheServingRouter(t *testing.T) {
	var timed bool
	probe := func(w http.ResponseWriter, _ *http.Request) {
		_, plain := w.(*httptest.ResponseRecorder)
		timed = !plain
		w.WriteHeader(http.StatusNoContent)
	}
	// A router with no static /api/v1 prefix: here /api/v1/resolve/main/x
	// really is a resolve of the repository "api/v1", while Handler()'s own
	// table routes it into /api/v1 and finds nothing. The two answers differ,
	// so the case shows which table was asked.
	r := chi.NewRouter()
	r.Use(boundHandlerTime)
	r.Get("/{ns}/{name}/resolve/{rev}/*", probe)
	r.Get("/model-meta/{kind}/{ns}/{name}/{rev}/*", probe)

	for _, tt := range []struct {
		path      string
		wantTimed bool
	}{
		{"/alice/bert/resolve/main/config.json", false},
		{"/api/v1/resolve/main/x", false},
		{"/model-meta/model/ns/n/main/resolve/evil.safetensors", true},
	} {
		timed = false
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", tt.path, nil))
		if timed != tt.wantTimed {
			t.Errorf("GET %s: timed = %v, want %v", tt.path, timed, tt.wantTimed)
		}
	}
}
