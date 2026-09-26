package api

import (
	"strings"
	"testing"
)

// icon.svg is a Next.js file convention (frontend/app/icon.svg) that serves
// /icon.svg the same way favicon.ico serves /favicon.ico -- it needs a spot on
// this list for the same reason favicon.ico does, and check-ui.mjs's scan of
// frontend/app/ only ever covered directories, so a top-level convention file
// like this one could go missing from both this list and
// frontend/lib/validation.ts's RESERVED_NAMESPACE_NAMES without either
// catching it.
func TestValidateNamespaceName_RejectsFrontendRouteConventionFiles(t *testing.T) {
	for _, name := range []string{"favicon.ico", "robots.txt", "sitemap.xml", "icon.svg"} {
		if err := validateNamespaceName(name); err != errReservedName {
			t.Errorf("validateNamespaceName(%q) = %v, want errReservedName", name, err)
		}
		// Case-insensitive, like every other namespace lookup.
		upper := strings.ToUpper(name)
		if err := validateNamespaceName(upper); err != errReservedName {
			t.Errorf("validateNamespaceName(%q) = %v, want errReservedName", upper, err)
		}
	}
}
