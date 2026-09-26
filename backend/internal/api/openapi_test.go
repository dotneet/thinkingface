package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/config"
)

// parseOpenAPIDoc decodes the embedded spec into a generic tree, the same way
// any consumer would; used by every sub-test below so a JSON-shape mistake is
// caught the same way a real client would hit it.
func parseOpenAPIDoc(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}
	return doc
}

// TestOpenAPI_Parses is (a): the embedded document parses and declares an
// OpenAPI 3.1.x version.
func TestOpenAPI_Parses(t *testing.T) {
	doc := parseOpenAPIDoc(t)
	version, _ := doc["openapi"].(string)
	if !regexp.MustCompile(`^3\.1\.\d+$`).MatchString(version) {
		t.Fatalf(`"openapi" = %q, want a 3.1.x version`, version)
	}
	if _, ok := doc["paths"].(map[string]any); !ok {
		t.Fatal(`"paths" is missing or not an object`)
	}
	if _, ok := doc["components"].(map[string]any); !ok {
		t.Fatal(`"components" is missing or not an object`)
	}
}

// httpMethodKeys are the OpenAPI path-item keys that name an operation, as
// opposed to "parameters", "summary" or any other path-item-level field.
var httpMethodKeys = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// normalizeRoutePattern collapses every "{name}" segment to "{}" so a
// documented path and a chi route pattern compare equal regardless of what
// either one calls its parameters.
var paramSeg = regexp.MustCompile(`\{[^}]*\}`)

func normalizeRoutePattern(p string) string {
	return paramSeg.ReplaceAllString(p, "{}")
}

// documentedRouteKeys walks the parsed document's "paths" object into a set
// of "METHOD normalized-path" keys.
func documentedRouteKeys(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	out := make(map[string]bool, len(paths)*2)
	for path, item := range paths {
		methods, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("path %q: path item is not an object", path)
		}
		norm := normalizeRoutePattern(path)
		for method := range methods {
			if !httpMethodKeys[method] {
				continue
			}
			out[strings.ToUpper(method)+" "+norm] = true
		}
	}
	return out
}

// routedKeys walks the chi router the server actually builds into the same
// "METHOD normalized-path" shape.
func routedKeys(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := chi.Walk(defaultRoutes(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out[method+" "+normalizeRoutePattern(route)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return out
}

// TestOpenAPI_DocumentedRoutesAreRouted is (b): every path+method this
// document describes actually exists in the chi router.
func TestOpenAPI_DocumentedRoutesAreRouted(t *testing.T) {
	doc := parseOpenAPIDoc(t)
	documented := documentedRouteKeys(t, doc)
	routed := routedKeys(t)

	var keys []string
	for k := range documented {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !routed[k] {
			t.Errorf("openapi.json documents %q, which is not a routed path+method", k)
		}
	}
}

// TestOpenAPI_ExperimentsAndTokensRoutesAreDocumented is (c): every routed
// /api/v1/experiments* or /api/v1/tokens* path+method is documented here.
// This is the direction that keeps the spec from silently going stale when
// a route is added or changed on those two prefixes -- CLAUDE.md invariant 1
// points back at this test.
func TestOpenAPI_ExperimentsAndTokensRoutesAreDocumented(t *testing.T) {
	doc := parseOpenAPIDoc(t)
	documented := documentedRouteKeys(t, doc)

	var missing []string
	err := chi.Walk(defaultRoutes(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/experiments") && !strings.HasPrefix(route, "/api/v1/tokens") {
			return nil
		}
		key := method + " " + normalizeRoutePattern(route)
		if !documented[key] {
			missing = append(missing, key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(missing)
	for _, k := range missing {
		t.Errorf("routed %q is not documented in openapi.json", k)
	}
}

// resolveJSONRef navigates "#/a/b/c" from the root of doc.
func resolveJSONRef(doc map[string]any, ref string) (any, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var cur any = doc
	for _, part := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// TestOpenAPI_RefsResolve is (d): every "$ref" in the document points at
// something that actually exists.
func TestOpenAPI_RefsResolve(t *testing.T) {
	doc := parseOpenAPIDoc(t)

	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch val := v.(type) {
		case map[string]any:
			if ref, ok := val["$ref"].(string); ok {
				if _, ok := resolveJSONRef(doc, ref); !ok {
					t.Errorf("%s: $ref %q does not resolve", path, ref)
				}
			}
			for k, vv := range val {
				if k == "$ref" {
					continue
				}
				walk(vv, path+"/"+k)
			}
		case []any:
			for i, vv := range val {
				walk(vv, path+"["+string(rune('0'+i%10))+"]")
			}
		}
	}
	walk(map[string]any(doc), "#")
}

// TestOpenAPI_Serves is (e): the handler answers with the right content
// type, a short public cache lifetime, and the "servers" entry patched to
// the configured public URL (or "/" when none is configured).
func TestOpenAPI_Serves(t *testing.T) {
	t.Run("with a configured public URL", func(t *testing.T) {
		s := &Server{cfg: &config.Config{PublicURL: "https://hub.example.test"}}
		w := httptest.NewRecorder()
		s.handleOpenAPI(w, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
			t.Errorf("Cache-Control = %q, want %q", cc, "public, max-age=300")
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("response body does not parse: %v", err)
		}
		servers, _ := body["servers"].([]any)
		if len(servers) != 1 {
			t.Fatalf("servers = %v, want exactly one entry", servers)
		}
		entry, _ := servers[0].(map[string]any)
		if entry["url"] != "https://hub.example.test" {
			t.Errorf("servers[0].url = %v, want the configured public URL", entry["url"])
		}
	})

	t.Run("with no configuration (zero Server)", func(t *testing.T) {
		s := &Server{}
		w := httptest.NewRecorder()
		s.handleOpenAPI(w, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))

		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("response body does not parse: %v", err)
		}
		servers, _ := body["servers"].([]any)
		entry, _ := servers[0].(map[string]any)
		if entry["url"] != "/" {
			t.Errorf("servers[0].url = %v, want \"/\" as the fallback", entry["url"])
		}
	})
}

// --- (f) reflection check: a declared schema's properties must be exactly
// the Go type's JSON field names, for the handful of types this is wired up
// for. It is the one check here that catches a field renamed in apitypes
// without the matching edit in openapi.json -- everything else in this file
// only checks routes, not shapes.

// jsonFieldNames returns the field names encoding/json would use for t,
// recursing into an anonymous embedded field with no json tag of its own
// (promoted/inlined, exactly as CreateTokenResponse embeds TokenItem).
func jsonFieldNames(t reflect.Type) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		tag, hasTag := f.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && (!hasTag || name == "") {
			out = append(out, jsonFieldNames(f.Type)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// schemaPropertyNames returns the property names a schema object declares,
// flattening "allOf" (including through a $ref, as CreateTokenResponse's
// allOf[0] is) so it can be compared against jsonFieldNames.
func schemaPropertyNames(t *testing.T, doc map[string]any, schema map[string]any) []string {
	t.Helper()
	var out []string
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, part := range allOf {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if ref, ok := pm["$ref"].(string); ok {
				resolved, ok := resolveJSONRef(doc, ref)
				if !ok {
					t.Fatalf("allOf $ref %q does not resolve", ref)
				}
				rm, ok := resolved.(map[string]any)
				if !ok {
					t.Fatalf("allOf $ref %q does not resolve to an object", ref)
				}
				out = append(out, schemaPropertyNames(t, doc, rm)...)
				continue
			}
			out = append(out, schemaPropertyNames(t, doc, pm)...)
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for k := range props {
			out = append(out, k)
		}
	}
	return out
}

func sortedUnique(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestOpenAPI_SchemasMatchGoTypes is (f).
func TestOpenAPI_SchemasMatchGoTypes(t *testing.T) {
	doc := parseOpenAPIDoc(t)
	schemas, ok := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if !ok {
		t.Fatal(`components.schemas is missing or not an object`)
	}

	cases := []struct {
		schema string
		goType reflect.Type
	}{
		{"ExpRun", reflect.TypeOf(apitypes.ExpRun{})},
		{"ExpRunListResponse", reflect.TypeOf(apitypes.ExpRunListResponse{})},
		{"ExpProject", reflect.TypeOf(apitypes.ExpProject{})},
		{"TokenItem", reflect.TypeOf(apitypes.TokenItem{})},
		{"CreateTokenResponse", reflect.TypeOf(apitypes.CreateTokenResponse{})},
		{"ExpNotesResponse", reflect.TypeOf(apitypes.ExpNotesResponse{})},
		{"ExpConfigDiffResponse", reflect.TypeOf(apitypes.ExpConfigDiffResponse{})},
		{"ServerInfo", reflect.TypeOf(apitypes.ServerInfo{})},
	}
	for _, c := range cases {
		t.Run(c.schema, func(t *testing.T) {
			schema, ok := schemas[c.schema].(map[string]any)
			if !ok {
				t.Fatalf("components.schemas.%s is missing or not an object", c.schema)
			}
			gotProps := sortedUnique(schemaPropertyNames(t, doc, schema))
			wantProps := sortedUnique(jsonFieldNames(c.goType))
			if !reflect.DeepEqual(gotProps, wantProps) {
				t.Errorf("%s: schema properties %v != %s's JSON field names %v",
					c.schema, gotProps, c.goType.Name(), wantProps)
			}
		})
	}
}
