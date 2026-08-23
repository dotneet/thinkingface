// Organisation CRUD, membership management, and the audit log
// (docs/dev/organization-design.md §7.1). The permission rules themselves live in
// authz.go; this file is the HTTP surface over them.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Audit actions (docs/dev/organization-design.md §5). The set is closed: the UI
// translates each one, so a new action means a new dictionary entry.
const (
	auditOrgCreated         = "org.created"
	auditOrgUpdated         = "org.updated"
	auditOrgDeleted         = "org.deleted"
	auditMemberAdded        = "member.added"
	auditMemberRoleChanged  = "member.role_changed"
	auditMemberRemoved      = "member.removed"
	auditMemberLeft         = "member.left"
	auditRepoCreated        = "repo.created"
	auditRepoDeleted        = "repo.deleted"
	auditRepoTransferredIn  = "repo.transferred_in"
	auditRepoTransferredOut = "repo.transferred_out"
	auditWebhookCreated     = "webhook.created"
	auditWebhookUpdated     = "webhook.updated"
	auditWebhookDeleted     = "webhook.deleted"
)

// defaultAuditPageSize is what the audit log returns without an explicit
// limit; the UI's "load more" button pages through with `before`.
const defaultAuditPageSize = 50

// audit records one administrative action against an organisation. It is
// best-effort by design: an operation that already succeeded must not be
// reported as failed because its audit line could not be written.
func (s *Server) audit(ctx context.Context, nsID int64, actor *store.User, action, target string, details map[string]any) {
	e := store.AuditEntry{Action: action, TargetName: target}
	if actor != nil {
		id := actor.ID
		e.ActorUserID = &id
		e.ActorName = actor.Username
	}
	if len(details) > 0 {
		if raw, err := json.Marshal(details); err == nil {
			e.Details = raw
		}
	}
	if err := s.store.AppendOrgAudit(ctx, nsID, e); err != nil {
		slog.Error("append org audit", "namespace_id", nsID, "action", action, "error", err)
	}
}

// auditMember is audit with the affected account recorded on the row too, so
// the log survives a rename of the target's account.
func (s *Server) auditMember(ctx context.Context, nsID int64, actor *store.User, action string, target *store.User, details map[string]any) {
	e := store.AuditEntry{Action: action, TargetName: target.Username}
	targetID := target.ID
	e.TargetUserID = &targetID
	if actor != nil {
		id := actor.ID
		e.ActorUserID = &id
		e.ActorName = actor.Username
	}
	if len(details) > 0 {
		if raw, err := json.Marshal(details); err == nil {
			e.Details = raw
		}
	}
	if err := s.store.AppendOrgAudit(ctx, nsID, e); err != nil {
		slog.Error("append org audit", "namespace_id", nsID, "action", action, "error", err)
	}
}

// auditNamespace records an action against ns only when ns is an
// organisation. Repository and webhook operations call this without caring
// whether they happen to be running in a personal namespace, where there is
// no organisation log to write to.
func (s *Server) auditNamespace(ctx context.Context, ns string, actor *store.User, action, target string, details map[string]any) {
	n, err := s.store.GetNamespace(ctx, ns)
	if err != nil || n.Kind != "org" {
		return
	}
	s.audit(ctx, n.ID, actor, action, target, details)
}

// ------------------------------------------------------------- conversions

func toOrgAPI(o *store.Org, viewer Role, numMembers, numRepos int64) apitypes.Org {
	return apitypes.Org{
		Name:              o.Name,
		DisplayName:       o.DisplayName,
		Description:       o.Description,
		Website:           o.Website,
		AvatarURL:         o.AvatarURL,
		MembersVisibility: apitypes.MembersVisibility(o.MembersVisibility),
		NumMembers:        numMembers,
		NumRepos:          numRepos,
		CreatedAt:         o.CreatedAt,
		ViewerRole:        viewer.orgRole(),
	}
}

// toOrgMemberAPI copies a membership row out. withEmail is false when a
// non-member is reading a `members_visibility = public` list: the roster is
// public, the addresses on it are not.
func toOrgMemberAPI(m *store.OrgMember, withEmail bool) apitypes.OrgMember {
	out := apitypes.OrgMember{
		Username:  m.Username,
		Role:      apitypes.OrgRole(m.Role),
		CreatedAt: m.CreatedAt,
	}
	if withEmail {
		out.Email = m.Email
	}
	return out
}

func toOrgAuditEntryAPI(e *store.AuditEntry) apitypes.OrgAuditEntry {
	details := map[string]any{}
	if len(e.Details) > 0 {
		_ = json.Unmarshal(e.Details, &details)
	}
	return apitypes.OrgAuditEntry{
		ID: e.ID, Actor: e.ActorName, Action: e.Action, Target: e.TargetName,
		Details: details, CreatedAt: e.CreatedAt,
	}
}

// viewerIDOf is the id whose memberships resolve the caller's role in a
// listing, or nil when signed out.
func viewerIDOf(user *store.User) *int64 {
	if user == nil {
		return nil
	}
	id := user.ID
	return &id
}

// ------------------------------------------------------------ validation

func validOrgRole(role apitypes.OrgRole) bool {
	switch role {
	case apitypes.OrgRoleAdmin, apitypes.OrgRoleWrite, apitypes.OrgRoleRead:
		return true
	}
	return false
}

// applyOrgUpdate validates an OrgUpdateRequest and turns it into the store's
// partial update. The four profile fields go through the same
// validateProfileFields as PATCH /me/profile, which is what closes the
// javascript: URL hole this endpoint used to have
// (docs/dev/namespace-design.md §10).
func applyOrgUpdate(req apitypes.OrgUpdateRequest) (store.OrgUpdate, map[string]any, error) {
	if err := validateProfileFields(req.DisplayName, req.Description, req.Website, req.AvatarURL); err != nil {
		return store.OrgUpdate{}, nil, err
	}
	out := store.OrgUpdate{}
	changed := map[string]any{}
	if req.DisplayName != nil {
		out.DisplayName = req.DisplayName
		changed["display_name"] = *req.DisplayName
	}
	if req.Description != nil {
		out.Description = req.Description
		changed["description"] = *req.Description
	}
	if req.Website != nil {
		out.Website = req.Website
		changed["website"] = *req.Website
	}
	if req.AvatarURL != nil {
		out.AvatarURL = req.AvatarURL
		changed["avatar_url"] = *req.AvatarURL
	}
	if req.MembersVisibility != nil {
		v := *req.MembersVisibility
		if v != apitypes.MembersVisibilityMembers && v != apitypes.MembersVisibilityPublic {
			return store.OrgUpdate{}, nil, errors.New("members_visibility must be members or public")
		}
		str := string(v)
		out.MembersVisibility = &str
		changed["members_visibility"] = str
	}
	return out, changed, nil
}

// --------------------------------------------------------------- handlers

// handleListOrgs answers GET /api/v1/orgs: the public directory. Every
// organisation is listed -- an organisation's existence is not a secret --
// but NumRepos counts only what the caller may see.
func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	orgs, total, err := s.store.ListOrgs(r.Context(), q.Get("search"), viewerIDOf(user), limit, offset)
	if err != nil {
		internalError(w, "list organisations", err)
		return
	}
	items := make([]apitypes.Org, 0, len(orgs))
	for i := range orgs {
		o := &orgs[i]
		role := roleFromString(o.Role)
		if user != nil && user.IsAdmin {
			role = RoleAdmin
		}
		items = append(items, toOrgAPI(&o.Org, role, o.NumMembers, o.NumRepos))
	}
	writeJSON(w, http.StatusOK, apitypes.OrgListResponse{Items: items, Total: total})
}

// handleMyOrgs answers GET /api/v1/me/orgs: the caller's own memberships.
// Site admins see only the organisations they actually belong to, since
// their blanket authority is not a membership (§3).
func (s *Server) handleMyOrgs(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	orgs, err := s.store.ListOrgsForUser(r.Context(), user.ID)
	if err != nil {
		internalError(w, "list organisations", err)
		return
	}
	items := make([]apitypes.Org, 0, len(orgs))
	for i := range orgs {
		o := &orgs[i]
		items = append(items, toOrgAPI(&o.Org, roleFromString(o.Role), o.NumMembers, o.NumRepos))
	}
	writeJSON(w, http.StatusOK, apitypes.OrgListResponse{Items: items, Total: int64(len(items))})
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	if s.cfg.OrgCreation == "admin" && !user.IsAdmin {
		writeError(w, http.StatusForbidden, "org_creation_disabled",
			"only site administrators may create organisations on this instance")
		return
	}
	var req apitypes.OrgCreateRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with a name") {
		return
	}
	if err := validateNamespaceName(req.Name); err != nil {
		writeNamespaceNameError(w, "organisation name", err)
		return
	}
	// The same profile limits as every later edit, so a value that could not
	// be saved by PATCH cannot be smuggled in at creation time either.
	if err := validateProfileFields(&req.DisplayName, &req.Description, nil, nil); err != nil {
		badRequest(w, err.Error())
		return
	}
	in := store.OrgUpdate{}
	if req.DisplayName != "" {
		in.DisplayName = &req.DisplayName
	}
	if req.Description != "" {
		in.Description = &req.Description
	}
	org, err := s.store.CreateOrg(r.Context(), req.Name, user.ID, in)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			conflict(w, "the name "+req.Name+" is already taken")
			return
		}
		internalError(w, "create organisation", err)
		return
	}
	s.audit(r.Context(), org.ID, user, auditOrgCreated, org.Name, nil)
	// The creator is the only member and its admin, and there is nothing in
	// it yet, so the counts are known without querying.
	writeJSON(w, http.StatusCreated, apitypes.OrgResponse{Org: toOrgAPI(org, RoleAdmin, 1, 0)})
}

// handleGetOrg answers GET /api/v1/orgs/{org}. Non-members get 200 with the
// public fields and viewer_role "": the organisation page is public.
func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	org, ok := s.loadOrg(w, r, chi.URLParam(r, "org"))
	if !ok {
		return
	}
	body, ok := s.orgResponse(w, r, org)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// orgResponse assembles the counts and the viewer's role around an
// organisation row.
func (s *Server) orgResponse(w http.ResponseWriter, r *http.Request, org *store.Org) (apitypes.OrgResponse, bool) {
	ctx := r.Context()
	user := currentUser(ctx)
	role, err := s.roleIn(ctx, user, org.Name)
	if err != nil {
		internalError(w, "check organisation role", err)
		return apitypes.OrgResponse{}, false
	}
	members, err := s.store.ListOrgMembers(ctx, org.ID)
	if err != nil {
		internalError(w, "count organisation members", err)
		return apitypes.OrgResponse{}, false
	}
	repos, err := s.store.CountOrgRepos(ctx, org.ID)
	if err != nil {
		internalError(w, "count organisation repositories", err)
		return apitypes.OrgResponse{}, false
	}
	return apitypes.OrgResponse{Org: toOrgAPI(org, role, int64(len(members)), repos)}, true
}

func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWrite(w, r); !ok {
		return
	}
	user, org, ok := s.requireOrgRole(w, r, chi.URLParam(r, "org"), RoleAdmin)
	if !ok {
		return
	}
	var req apitypes.OrgUpdateRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}
	update, changed, err := applyOrgUpdate(req)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	updated, err := s.store.UpdateOrg(r.Context(), org.ID, update)
	if err != nil {
		handleStoreError(w, "update organisation", err)
		return
	}
	if len(changed) > 0 {
		s.audit(r.Context(), org.ID, user, auditOrgUpdated, org.Name, changed)
	}
	body, ok := s.orgResponse(w, r, updated)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWrite(w, r); !ok {
		return
	}
	user, org, ok := s.requireOrgRole(w, r, chi.URLParam(r, "org"), RoleAdmin)
	if !ok {
		return
	}
	if err := s.store.DeleteOrg(r.Context(), org.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "has_repositories",
				"delete or transfer the organisation's repositories first")
			return
		}
		handleStoreError(w, "delete organisation", err)
		return
	}
	// org.deleted is not written to org_audit_log: the log is scoped to the
	// namespace and cascades away with it, so the row would be deleted in
	// the same statement that created it. The process log keeps the record.
	slog.Info("organisation deleted", "action", auditOrgDeleted,
		"organisation", org.Name, "actor", user.Username)
	w.WriteHeader(http.StatusNoContent)
}

// handleListOrgMembers answers GET /api/v1/orgs/{org}/members. Members
// always see the roster; everyone else only when members_visibility is
// "public", and then without email addresses.
func (s *Server) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	org, ok := s.loadOrg(w, r, chi.URLParam(r, "org"))
	if !ok {
		return
	}
	ctx := r.Context()
	role, err := s.roleIn(ctx, currentUser(ctx), org.Name)
	if err != nil {
		internalError(w, "check organisation role", err)
		return
	}
	if role < RoleRead && org.MembersVisibility != "public" {
		forbidden(w, "the member list of "+org.Name+" is visible to its members only")
		return
	}
	members, err := s.store.ListOrgMembers(ctx, org.ID)
	if err != nil {
		internalError(w, "list organisation members", err)
		return
	}
	items := make([]apitypes.OrgMember, 0, len(members))
	for i := range members {
		items = append(items, toOrgMemberAPI(&members[i], role >= RoleRead))
	}
	writeJSON(w, http.StatusOK, apitypes.OrgMembersResponse{Items: items})
}

func (s *Server) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWrite(w, r); !ok {
		return
	}
	user, org, ok := s.requireOrgRole(w, r, chi.URLParam(r, "org"), RoleAdmin)
	if !ok {
		return
	}
	var req apitypes.OrgMemberAddRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with a username") {
		return
	}
	if req.Role == "" {
		req.Role = apitypes.OrgRoleRead
	}
	if !validOrgRole(req.Role) {
		badRequest(w, "role must be admin, write or read")
		return
	}
	target, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "no user named "+req.Username)
			return
		}
		internalError(w, "load user", err)
		return
	}
	member, err := s.store.AddOrgMember(r.Context(), org.ID, target.ID, string(req.Role), user.ID)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "already_member",
				req.Username+" already belongs to "+org.Name)
			return
		}
		internalError(w, "add organisation member", err)
		return
	}
	s.auditMember(r.Context(), org.ID, user, auditMemberAdded, target,
		map[string]any{"role": string(req.Role)})
	writeJSON(w, http.StatusCreated, apitypes.OrgMemberResponse{Member: toOrgMemberAPI(member, true)})
}

func (s *Server) handleUpdateOrgMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWrite(w, r); !ok {
		return
	}
	user, org, ok := s.requireOrgRole(w, r, chi.URLParam(r, "org"), RoleAdmin)
	if !ok {
		return
	}
	target, ok := s.loadOrgMemberTarget(w, r, org, chi.URLParam(r, "username"))
	if !ok {
		return
	}
	var req apitypes.OrgMemberUpdateRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with a role") {
		return
	}
	if !validOrgRole(req.Role) {
		badRequest(w, "role must be admin, write or read")
		return
	}
	before, err := s.store.GetOrgMember(r.Context(), org.ID, target.ID)
	if err != nil {
		handleStoreError(w, "load organisation member", err)
		return
	}
	member, err := s.store.UpdateOrgMemberRole(r.Context(), org.ID, target.ID, string(req.Role))
	if err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			writeLastAdminError(w, org.Name)
			return
		}
		handleStoreError(w, "update organisation member", err)
		return
	}
	if before.Role != member.Role {
		s.auditMember(r.Context(), org.ID, user, auditMemberRoleChanged, target,
			map[string]any{"from": before.Role, "to": member.Role})
	}
	writeJSON(w, http.StatusOK, apitypes.OrgMemberResponse{Member: toOrgMemberAPI(member, true)})
}

// handleRemoveOrgMember answers DELETE /api/v1/orgs/{org}/members/{username},
// which is both "an admin removes someone" and "a member leaves": leaving is
// the same call aimed at yourself (docs/dev/organization-design.md §5).
func (s *Server) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	org, ok := s.loadOrg(w, r, chi.URLParam(r, "org"))
	if !ok {
		return
	}
	username := chi.URLParam(r, "username")
	leaving := username == actor.Username

	role, err := s.roleIn(r.Context(), actor, org.Name)
	if err != nil {
		internalError(w, "check organisation role", err)
		return
	}
	if role < RoleAdmin && !leaving {
		forbidden(w, "you must have admin access to organisation "+org.Name)
		return
	}
	// A site admin is not a member, so "leaving" only ever means the caller's
	// own org_members row.
	if leaving && role == RoleNone {
		notFound(w, username+" is not a member of "+org.Name)
		return
	}

	target, ok := s.loadOrgMemberTarget(w, r, org, username)
	if !ok {
		return
	}
	if err := s.store.RemoveOrgMember(r.Context(), org.ID, target.ID); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			writeLastAdminError(w, org.Name)
			return
		}
		handleStoreError(w, "remove organisation member", err)
		return
	}
	action := auditMemberRemoved
	if leaving {
		action = auditMemberLeft
	}
	s.auditMember(r.Context(), org.ID, actor, action, target, nil)
	w.WriteHeader(http.StatusNoContent)
}

// loadOrgMemberTarget resolves the {username} path parameter to an account
// that actually belongs to the organisation, so "no such user" and "not a
// member" both read as a 404 against the membership URL.
func (s *Server) loadOrgMemberTarget(w http.ResponseWriter, r *http.Request, org *store.Org, username string) (*store.User, bool) {
	target, err := s.store.GetUserByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, username+" is not a member of "+org.Name)
			return nil, false
		}
		internalError(w, "load user", err)
		return nil, false
	}
	if _, err := s.store.GetOrgMember(r.Context(), org.ID, target.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, username+" is not a member of "+org.Name)
			return nil, false
		}
		internalError(w, "load organisation member", err)
		return nil, false
	}
	return target, true
}

func writeLastAdminError(w http.ResponseWriter, org string) {
	writeError(w, http.StatusConflict, "last_admin",
		"appoint another admin before removing the last one from "+org)
}

func (s *Server) handleOrgAuditLog(w http.ResponseWriter, r *http.Request) {
	_, org, ok := s.requireOrgRole(w, r, chi.URLParam(r, "org"), RoleAdmin)
	if !ok {
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = defaultAuditPageSize
	}
	entries, err := s.store.ListOrgAudit(r.Context(), org.ID, before, limit)
	if err != nil {
		internalError(w, "list audit log", err)
		return
	}
	items := make([]apitypes.OrgAuditEntry, 0, len(entries))
	for i := range entries {
		items = append(items, toOrgAuditEntryAPI(&entries[i]))
	}
	// A full page implies there may be more; a short one is the end.
	var next int64
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, apitypes.OrgAuditLogResponse{Items: items, NextBefore: next})
}

// ------------------------------------------------------------ HF-compatible

// handleHFOrgMembers answers GET /api/organizations/{org}/members, which is
// what huggingface_hub's HfApi.list_organization_members() calls. HF returns
// a bare list of accounts with no roles; the authorization is the same as
// the UI's own member list (docs/dev/organization-design.md §7.2).
func (s *Server) handleHFOrgMembers(w http.ResponseWriter, r *http.Request) {
	org, ok := s.loadOrg(w, r, chi.URLParam(r, "org"))
	if !ok {
		return
	}
	ctx := r.Context()
	role, err := s.roleIn(ctx, currentUser(ctx), org.Name)
	if err != nil {
		internalError(w, "check organisation role", err)
		return
	}
	if role < RoleRead && org.MembersVisibility != "public" {
		forbidden(w, "the member list of "+org.Name+" is visible to its members only")
		return
	}
	members, err := s.store.ListOrgMembers(ctx, org.ID)
	if err != nil {
		internalError(w, "list organisation members", err)
		return
	}
	out := make([]map[string]any, 0, len(members))
	for i := range members {
		out = append(out, map[string]any{
			"user":      members[i].Username,
			"fullname":  members[i].Username,
			"avatarUrl": "",
			"type":      "user",
			"isPro":     false,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// whoamiOrgs builds the `orgs` array of GET /api/whoami-v2 in HuggingFace's
// shape (docs/dev/organization-design.md §7.2). `hf auth whoami` prints name and
// roleInOrg from it.
func (s *Server) whoamiOrgs(ctx context.Context, user *store.User) []map[string]any {
	orgs, err := s.store.ListOrgsForUser(ctx, user.ID)
	if err != nil {
		slog.Error("list organisations for whoami", "user", user.Username, "error", err)
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(orgs))
	for i := range orgs {
		o := &orgs[i]
		fullname := o.DisplayName
		if fullname == "" {
			fullname = o.Name
		}
		out = append(out, map[string]any{
			"type":         "org",
			"id":           strconv.FormatInt(o.ID, 10),
			"name":         o.Name,
			"fullname":     fullname,
			"email":        nil,
			"canPay":       false,
			"periodEnd":    nil,
			"avatarUrl":    o.AvatarURL,
			"isEnterprise": false,
			"roleInOrg":    o.Role,
		})
	}
	return out
}
