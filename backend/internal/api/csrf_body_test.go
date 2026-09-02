// The two halves of the CSRF backstop that used to be missing.
//
// requireSameOrigin gated on the session cookie alone, but HTTP Basic is
// ambient too: unauthorized() answers `WWW-Authenticate: Basic`, so a browser
// that has ever filled in that dialog re-attaches the credential to this origin
// by itself -- including on a cross-site top-level form POST. And decodeJSON
// read a body of any Content-Type, so a `<form enctype="text/plain">` could
// deliver a valid JSON document to an endpoint like POST /api/v1/repos without
// tripping a CORS preflight. Either alone is enough; both together were a
// working cross-site write.

package api

import (
	"context"
	"net/http"
	"testing"
)

// createRepo is the body a cross-site form would be shaped to deliver.
func createRepoBody(name string) map[string]any {
	return map[string]any{"kind": "model", "namespace": "alice", "name": name}
}

func repoExists(t *testing.T, f *secFixture, name string) bool {
	t.Helper()
	_, err := f.st.GetRepo(context.Background(), "model", "alice", name)
	return err == nil
}

// The three Content-Types a browser form can produce are exactly the ones no
// JSON endpoint should read. text/plain is the dangerous one: its body is
// whatever the attacker types, so it can be valid JSON.
func TestDecodeJSON_RefusesTheContentTypesAFormCanSend(t *testing.T) {
	for _, ct := range []string{
		"text/plain",
		"text/plain;charset=UTF-8",
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
	} {
		t.Run(ct, func(t *testing.T) {
			f := newSecFixture(t)
			alice := f.user("alice", "correct horse battery")
			rec := f.do(secRequest{
				method: "POST", path: "/api/v1/repos",
				body: createRepoBody("forged"),
				headers: map[string]string{
					"Content-Type":  ct,
					"Authorization": "Bearer " + f.token(alice, "write"),
				},
			})
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, body = %s; want 415", rec.Code, rec.Body.String())
			}
			if repoExists(t, f, "forged") {
				t.Error("the repository was created from a form-encodable body")
			}
		})
	}
}

// What real clients send has to keep working: application/json from the Web UI
// and huggingface_hub, the +json suffix git-lfs uses, and no Content-Type at
// all from curl, the e2e suite and this package's own older tests. A browser
// always sets the header, so an absent one is never a form.
func TestDecodeJSON_AcceptsWhatRealClientsSend(t *testing.T) {
	for name, ct := range map[string]string{
		"application/json":            "application/json",
		"with charset":                "application/json; charset=utf-8",
		"vendor json":                 "application/vnd.git-lfs+json",
		"absent (curl, git, the e2e)": "",
	} {
		t.Run(name, func(t *testing.T) {
			f := newSecFixture(t)
			alice := f.user("alice", "correct horse battery")
			headers := map[string]string{"Authorization": "Bearer " + f.token(alice, "write")}
			if ct != "" {
				headers["Content-Type"] = ct
			} else {
				// f.do sets application/json by default, so the empty case has
				// to be asked for explicitly.
				headers["Content-Type"] = ""
			}
			rec := f.do(secRequest{
				method: "POST", path: "/api/v1/repos",
				body: createRepoBody("legit"), headers: headers,
			})
			if rec.Code >= 400 {
				t.Fatalf("status = %d, body = %s; want the request accepted", rec.Code, rec.Body.String())
			}
			if !repoExists(t, f, "legit") {
				t.Error("the repository was not created")
			}
		})
	}
}

// A Basic *password* is a credential the browser attaches on its own, so a
// state-changing request carrying one from an origin this server does not know
// is refused exactly as a cookie-authenticated one is.
func TestSameOrigin_CoversBasicPasswordAuthentication(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	hostile := f.do(secRequest{
		method: "POST", path: "/api/v1/repos",
		body: createRepoBody("forged"),
		headers: map[string]string{
			"Authorization": basicAuth("alice", "correct horse battery"),
			"Origin":        "https://evil.example",
		},
	})
	if hostile.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s; want 403", hostile.Code, hostile.Body.String())
	}
	if repoExists(t, f, "forged") {
		t.Error("a cross-origin Basic-authenticated write went through")
	}
}

// The other side of the same rule: `git`, `curl` and the e2e suite send no
// Origin at all, which is what identifies a caller no page can steer. Refusing
// those would break every non-browser client that authenticates with a
// password.
func TestSameOrigin_BasicPasswordWithoutAnOriginStillWorks(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	rec := f.do(secRequest{
		method: "POST", path: "/api/v1/repos",
		body:    createRepoBody("legit"),
		headers: map[string]string{"Authorization": basicAuth("alice", "correct horse battery")},
	})
	if rec.Code >= 400 {
		t.Fatalf("status = %d, body = %s; want the request accepted", rec.Code, rec.Body.String())
	}
	if !repoExists(t, f, "legit") {
		t.Error("the repository was not created")
	}
}

// A Bearer token is not ambient -- a page has to put it in a header, which a
// form cannot do and a preflight would catch -- so it is deliberately not
// gated. `git-lfs` and huggingface_hub authenticate this way from wherever
// they happen to run.
func TestSameOrigin_BearerTokenIsNotGatedByOrigin(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")

	rec := f.do(secRequest{
		method: "POST", path: "/api/v1/repos",
		body: createRepoBody("legit"),
		headers: map[string]string{
			"Authorization": "Bearer " + f.token(alice, "write"),
			"Origin":        "https://evil.example",
		},
	})
	if rec.Code >= 400 {
		t.Fatalf("status = %d, body = %s; want the request accepted", rec.Code, rec.Body.String())
	}
}
