// Authorization: one effective-role function every permission check in this
// package funnels through (docs/organization-design.md §3.1, §4). Before it
// existed the same question was asked three different ways -- "owns the
// namespace", "can write the namespace", "is an org admin" -- which is how a
// `read` member ended up able to see a private repository in a listing but
// not open it.

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Role is a caller's effective authority over one namespace. The values are
// ordered, so every check reads as "at least this much".
type Role int

const (
	// RoleNone is a signed-out caller, or one with no relationship to the
	// namespace.
	RoleNone Role = iota
	// RoleRead may see everything in the namespace, including private
	// repositories, but change nothing.
	RoleRead
	// RoleWrite may additionally create repositories and push.
	RoleWrite
	// RoleAdmin may additionally delete and transfer repositories, manage
	// webhooks, and administer the organisation itself.
	RoleAdmin
)

// roleFromString maps a stored org_members role onto Role. An unknown value
// (only reachable if the CHECK constraint were ever loosened) is RoleNone,
// which fails closed.
func roleFromString(role string) Role {
	switch role {
	case "admin":
		return RoleAdmin
	case "write":
		return RoleWrite
	case "read":
		return RoleRead
	default:
		return RoleNone
	}
}

// String renders the role the way the API spells it; RoleNone is "".
func (r Role) String() string {
	switch r {
	case RoleAdmin:
		return "admin"
	case RoleWrite:
		return "write"
	case RoleRead:
		return "read"
	default:
		return ""
	}
}

func (r Role) orgRole() apitypes.OrgRole { return apitypes.OrgRole(r.String()) }

// roleIn resolves user's effective role in the namespace named ns:
//
//	site admin                          -> RoleAdmin
//	kind='user' and the namespace's own -> RoleAdmin
//	kind='org' with an org_members row  -> that role
//	anything else                       -> RoleNone
//
// A site admin answers RoleAdmin without touching the database, and without
// implying an org_members row: they never appear in a member list or in
// whoami's orgs (docs/organization-design.md §3).
//
// A namespace that does not exist is RoleNone with a nil error: callers that
// care about existence look the namespace up themselves.
func (s *Server) roleIn(ctx context.Context, user *store.User, ns string) (Role, error) {
	if user == nil {
		return RoleNone, nil
	}
	if user.IsAdmin {
		return RoleAdmin, nil
	}
	nr, err := s.store.NamespaceRoleFor(ctx, user.ID, ns)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return RoleNone, nil
		}
		return RoleNone, err
	}
	return roleFromString(nr.Role), nil
}

// canWrite reports whether the caller may push to, commit to, or otherwise
// modify the repository's contents.
// canWrite reports whether the caller may change this repository right now.
// An archived repository is read-only for everyone, so this is false for it
// regardless of permissions -- which is what makes every editing affordance
// (`can_write` in RepoDetail, the LFS proxy upload, verify) disappear at
// once. Use canWriteIgnoringArchive where the permission alone is the
// question.
func (s *Server) canWrite(ctx context.Context, repo *store.Repo) bool {
	if repo.Archived() {
		return false
	}
	return s.canWriteIgnoringArchive(ctx, repo)
}

func (s *Server) canWriteIgnoringArchive(ctx context.Context, repo *store.Repo) bool {
	return s.hasRole(ctx, repo.Namespace, RoleWrite, true)
}

// canAdmin reports whether the caller may delete or transfer the repository
// or manage its namespace's webhooks. In a personal namespace this is the
// owner, so the bar is unchanged there; under an organisation it excludes
// `write` members (docs/organization-design.md §4).
func (s *Server) canAdmin(ctx context.Context, repo *store.Repo) bool {
	return s.hasRole(ctx, repo.Namespace, RoleAdmin, true)
}

// hasRole is the shared body of the three checks above. needWriteScope also
// rejects a read-only token, which is orthogonal to the role: a namespace
// admin holding a read token may still only read.
func (s *Server) hasRole(ctx context.Context, ns string, min Role, needWriteScope bool) bool {
	user := currentUser(ctx)
	if user == nil {
		return false
	}
	if needWriteScope && currentScope(ctx) != "write" {
		return false
	}
	role, err := s.roleIn(ctx, user, ns)
	return err == nil && role >= min
}

// loadOrg resolves the {org} URL parameter. A user namespace of that name is
// a 404: organisations and accounts share one name space, but only
// organisations answer on /orgs.
func (s *Server) loadOrg(w http.ResponseWriter, r *http.Request, name string) (*store.Org, bool) {
	org, err := s.store.GetOrg(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "organisation "+name+" not found")
		} else {
			internalError(w, "load organisation", err)
		}
		return nil, false
	}
	return org, true
}

// requireOrgRole resolves the organisation named in the URL and checks the
// caller holds at least min in it. Membership itself is not secret -- the
// organisation exists publicly -- so an insufficient role is 403, not 404.
//
// It requires authentication but not write scope: handlers that mutate call
// requireWrite first, so a read-only token is refused before any of this
// runs.
func (s *Server) requireOrgRole(w http.ResponseWriter, r *http.Request, orgName string, min Role) (*store.User, *store.Org, bool) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return nil, nil, false
	}
	org, ok := s.loadOrg(w, r, orgName)
	if !ok {
		return nil, nil, false
	}
	role, err := s.roleIn(r.Context(), user, org.Name)
	if err != nil {
		internalError(w, "check organisation role", err)
		return nil, nil, false
	}
	if role < min {
		forbidden(w, "you must have "+min.String()+" access to organisation "+org.Name)
		return nil, nil, false
	}
	return user, org, true
}
