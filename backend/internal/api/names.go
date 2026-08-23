// Reserved namespace names (docs/dev/organization-design.md §6.3). Repository
// names are unaffected: "alice/models" is a perfectly good repository, only
// a top-level namespace called "models" is not.

package api

import (
	"errors"
	"net/http"
	"strings"
)

// errReservedName is what validateNamespaceName returns for a name on the
// list below, so callers can answer with the `reserved_name` error type
// rather than a generic bad request.
var errReservedName = errors.New("is reserved and cannot be used as a namespace")

// reservedNamespaceNames are the names a namespace may not take because a
// namespace occupies the first URL segment on both sides of the system:
// the frontend's own routes (app/), this server's /{ns}/{name} repository
// transport, and the HF-compatible /datasets/{ns}/{name} shape.
//
// Mirrored in frontend/lib/validation.ts -- keep the two in step
// (frontend/scripts/check-ui.mjs verifies the two lists agree and that every
// static top-level route under frontend/app/ is listed).
//
// Existing accounts holding one of these names are left alone; only new
// namespaces are refused. "admin" is deliberately absent: it is the default
// seeded account's username (TF_ADMIN_USERNAME) and there is no /admin route.
var reservedNamespaceNames = map[string]bool{
	"api": true, "apis": true, "datasets": true, "models": true, "spaces": true,
	"experiments": true, "orgs": true, "organizations": true, "settings": true,
	"new": true, "login": true, "logout": true, "signup": true, "styleguide": true,
	"healthz": true, "static": true, "_next": true, "assets": true,
	"raw": true, "resolve": true, "lfs": true, "info": true, "git": true,
	"webhooks": true, "transfers": true, "me": true, "whoami-v2": true,
	// Frontend-only assets and routes (docs/dev/namespace-design.md §9).
	"favicon.ico": true, "robots.txt": true, "sitemap.xml": true, "duckdb": true,
	"public": true, "users": true, "namespaces": true, "profile": true, "search": true,
}

// validateNamespaceName is validateName plus the reserved list. It guards the
// two places a namespace comes into existence -- sign-up and organisation
// creation -- and nothing else.
func validateNamespaceName(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if reservedNamespaceNames[strings.ToLower(name)] {
		return errReservedName
	}
	return nil
}

// writeNamespaceNameError answers a validateNamespaceName failure, giving a
// reserved name its own error type so the UI can say something better than
// "bad request".
func writeNamespaceNameError(w http.ResponseWriter, field string, err error) {
	if errors.Is(err, errReservedName) {
		writeError(w, http.StatusBadRequest, "reserved_name", field+" "+err.Error())
		return
	}
	badRequest(w, field+" "+err.Error())
}
