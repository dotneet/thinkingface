// Namespaces: the one endpoint that answers "does this name exist, and what
// does it hold" for a user account and an organisation alike
// (docs/dev/namespace-design.md §7), the self-service profile edit behind
// /settings/profile, and the HuggingFace-compatible overview endpoints that
// read the same profile.

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Profile field ceilings (docs/dev/namespace-design.md §10). display_name and
// description are counted in runes because they are prose typed by a person:
// a byte limit would cut a Japanese bio to a third of an English one. The
// URLs are counted in bytes, which is what a URL length limit means.
const (
	maxDisplayNameRunes = 96
	maxDescriptionRunes = 1024
	maxProfileURLBytes  = 2048
)

// validateProfileFields checks the four profile columns shared by users and
// organisations. Only non-nil fields are checked: these are partial updates,
// and a field that is not being written cannot be made invalid.
//
// The URL scheme check is the security-relevant one: website and avatar_url
// are rendered into <a href> and <img src>, so a javascript: or data: value
// would be stored XSS waiting for a click (§10). It applies to organisation
// updates too -- that path had no validation at all before.
func validateProfileFields(displayName, description, website, avatarURL *string) error {
	if displayName != nil && utf8.RuneCountInString(*displayName) > maxDisplayNameRunes {
		return fmt.Errorf("display_name must be at most %d characters", maxDisplayNameRunes)
	}
	if description != nil && utf8.RuneCountInString(*description) > maxDescriptionRunes {
		return fmt.Errorf("description must be at most %d characters", maxDescriptionRunes)
	}
	if err := validateProfileURL("website", website); err != nil {
		return err
	}
	return validateProfileURL("avatar_url", avatarURL)
}

// validateProfileURL accepts an empty value (the field is being cleared) or an
// http/https URL within the length limit, and nothing else.
func validateProfileURL(field string, v *string) error {
	if v == nil || *v == "" {
		return nil
	}
	if len(*v) > maxProfileURLBytes {
		return fmt.Errorf("%s must be at most %d bytes", field, maxProfileURLBytes)
	}
	// Schemes are case-insensitive (RFC 3986 §3.1), so "HTTPS://" passes;
	// anything else -- javascript:, data:, scheme-relative "//host" -- does not.
	lower := strings.ToLower(*v)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("%s must start with http:// or https://", field)
	}
	return nil
}

// ------------------------------------------------------------------ helpers

// namespaceProfile loads a namespace's profile for read-only use, answering
// nil rather than an error: whoami and the account shape degrade to the bare
// username if the row cannot be read, which is better than failing a login.
func (s *Server) namespaceProfile(ctx context.Context, name string) *store.NamespaceProfile {
	p, err := s.store.GetNamespaceProfile(ctx, name)
	if err != nil {
		return nil
	}
	return p
}

// displayNameOr is the HF `fullname` rule: the profile's display name when it
// has one, the namespace name otherwise (docs/dev/namespace-design.md §5.3).
func displayNameOr(p *store.NamespaceProfile, name string) string {
	if p != nil && p.DisplayName != "" {
		return p.DisplayName
	}
	return name
}

// loadNamespaceOfKind answers the HF overview lookups: the profile when it
// exists and has the expected kind, otherwise a 404 (a user name asked for as
// an organisation is "not found", the way HF answers) -- and a 500, not a
// 404, when the store itself fails.
func (s *Server) loadNamespaceOfKind(w http.ResponseWriter, r *http.Request, name string, kind apitypes.NamespaceKind, what string) (*store.NamespaceProfile, bool) {
	p, err := s.store.GetNamespaceProfile(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, what+" "+name+" not found")
		} else {
			internalError(w, "load namespace", err)
		}
		return nil, false
	}
	if p.Kind != string(kind) {
		notFound(w, what+" "+name+" not found")
		return nil, false
	}
	return p, true
}

func avatarOf(p *store.NamespaceProfile) string {
	if p == nil {
		return ""
	}
	return p.AvatarURL
}

// namespaceResponse assembles the counts and the caller's role around a
// profile row. The organisation-only fields stay zero for a user namespace,
// so the UI can render one shape for both kinds.
func (s *Server) namespaceResponse(w http.ResponseWriter, r *http.Request, p *store.NamespaceProfile) (apitypes.NamespaceResponse, bool) {
	ctx := r.Context()
	counts, err := s.store.CountNamespaceResources(ctx, p.ID)
	if err != nil {
		internalError(w, "count namespace resources", err)
		return apitypes.NamespaceResponse{}, false
	}
	viewer := currentUser(ctx)
	role, err := s.roleIn(ctx, viewer, p.Name)
	if err != nil {
		internalError(w, "check namespace role", err)
		return apitypes.NamespaceResponse{}, false
	}
	// A site admin is "admin" everywhere (roleIn), but the only profile
	// editor is PATCH /me/profile, which edits the caller's own namespace; so
	// for a user namespace can_edit is true for its owner alone, not for a
	// site admin looking at somebody else (docs/dev/namespace-design.md §10).
	isOwner := viewer != nil && strings.EqualFold(viewer.Username, p.Name)
	canEdit := role == RoleAdmin && (p.Kind == string(apitypes.NamespaceKindOrg) || isOwner)
	out := apitypes.NamespaceProfile{
		Name:           p.Name,
		Kind:           apitypes.NamespaceKind(p.Kind),
		DisplayName:    p.DisplayName,
		Description:    p.Description,
		Website:        p.Website,
		AvatarURL:      p.AvatarURL,
		CreatedAt:      p.CreatedAt,
		NumModels:      counts.Models,
		NumDatasets:    counts.Datasets,
		NumExperiments: counts.Experiments,
		ViewerRole:     role.orgRole(),
		CanEdit:        canEdit,
	}
	if p.Kind == string(apitypes.NamespaceKindOrg) {
		out.NumMembers = counts.Members
		out.MembersVisibility = apitypes.MembersVisibility(p.MembersVisibility)
	}
	return apitypes.NamespaceResponse{Namespace: out}, true
}

// ----------------------------------------------------------------- handlers

// handleGetNamespace answers GET /api/v1/namespaces/{ns}. It is public: a
// namespace's existence is already visible in every repository URL, and the
// sign-up form's availability check reads it unauthenticated
// (docs/dev/namespace-design.md §10).
//
// Only the name *syntax* is checked before the lookup, not the reserved
// list: the reserved list guards creation (docs/dev/namespace-design.md §9), and
// an account that predates an entry on it -- or the seeded `admin` user,
// should a deployment ever reserve that -- still exists and must still
// answer here. A reserved name nobody holds is a plain 404 from the lookup.
func (s *Server) handleGetNamespace(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "ns")
	if err := validateName(name); err != nil {
		notFound(w, "namespace "+name+" not found")
		return
	}
	p, err := s.store.GetNamespaceProfile(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "namespace "+name+" not found")
		} else {
			internalError(w, "load namespace", err)
		}
		return
	}
	body, ok := s.namespaceResponse(w, r, p)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleUpdateMyProfile answers PATCH /api/v1/me/profile: the caller edits
// their own user namespace's profile columns, and nothing else. A token works
// as well as a session so CI can set a profile, but a read-scoped one does
// not (requireWrite). There is deliberately no route for editing somebody
// else's profile, site admin included (docs/dev/namespace-design.md §10).
func (s *Server) handleUpdateMyProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req apitypes.NamespaceProfileUpdate
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}
	if err := validateProfileFields(req.DisplayName, req.Description, req.Website, req.AvatarURL); err != nil {
		badRequest(w, err.Error())
		return
	}
	// The user's namespace is created with the account and shares its name,
	// so this always resolves.
	ns, err := s.store.GetNamespaceProfile(r.Context(), user.Username)
	if err != nil {
		handleStoreError(w, "load own namespace", err)
		return
	}
	updated, err := s.store.UpdateNamespaceProfile(r.Context(), ns.ID, store.NamespaceUpdate{
		DisplayName: req.DisplayName,
		Description: req.Description,
		Website:     req.Website,
		AvatarURL:   req.AvatarURL,
	})
	if err != nil {
		handleStoreError(w, "update profile", err)
		return
	}
	body, ok := s.namespaceResponse(w, r, updated)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// ------------------------------------------------------------ HF-compatible

// handleHFUserOverview answers GET /api/users/{username}/overview, which is
// what huggingface_hub's HfApi.get_user_overview() calls
// (docs/dev/namespace-design.md §7.2). Organisations answer on their own endpoint,
// so an organisation name is a 404 here -- the same way HF behaves.
//
// The follower/like counters are not modelled at all and are reported as 0
// rather than omitted: huggingface_hub's User dataclass tolerates missing
// keys, but a caller reading `num_likes` should get a number.
func (s *Server) handleHFUserOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.loadNamespaceOfKind(w, r, chi.URLParam(r, "username"), apitypes.NamespaceKindUser, "user")
	if !ok {
		return
	}
	counts, err := s.store.CountNamespaceResources(ctx, p.ID)
	if err != nil {
		internalError(w, "count namespace resources", err)
		return
	}
	// The namespace row carries the profile; the account row is only needed
	// for the organisation memberships.
	user, err := s.store.GetUserByUsername(ctx, p.Name)
	if err != nil {
		handleStoreError(w, "load user", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         p.Name,
		"fullname":     displayNameOr(p, p.Name),
		"avatarUrl":    p.AvatarURL,
		"details":      p.Description,
		"type":         "user",
		"numModels":    counts.Models,
		"numDatasets":  counts.Datasets + counts.Experiments,
		"numSpaces":    0,
		"numLikes":     0,
		"numFollowers": 0,
		"numFollowing": 0,
		"isPro":        false,
		"orgs":         s.whoamiOrgs(ctx, user),
	})
}

// handleHFOrgOverview answers GET /api/organizations/{org}/overview for
// HfApi.get_organization_overview(). A user namespace of that name is a 404,
// matching /api/v1/orgs/{org}.
func (s *Server) handleHFOrgOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.loadNamespaceOfKind(w, r, chi.URLParam(r, "org"), apitypes.NamespaceKindOrg, "organisation")
	if !ok {
		return
	}
	counts, err := s.store.CountNamespaceResources(ctx, p.ID)
	if err != nil {
		internalError(w, "count namespace resources", err)
		return
	}
	// numUsers is the member count regardless of members_visibility: the
	// size of an organisation is not what that setting hides -- the roster is
	// (docs/dev/organization-design.md §6.1).
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         p.Name,
		"fullname":     displayNameOr(p, p.Name),
		"avatarUrl":    p.AvatarURL,
		"details":      p.Description,
		"numUsers":     counts.Members,
		"numModels":    counts.Models,
		"numDatasets":  counts.Datasets + counts.Experiments,
		"numSpaces":    0,
		"numFollowers": 0,
		"isVerified":   false,
	})
}
