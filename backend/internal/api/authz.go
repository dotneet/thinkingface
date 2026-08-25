// Authorization: one effective-role function every permission check in this
// package funnels through (docs/dev/organization-design.md §3.1, §4). Before it
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
// whoami's orgs (docs/dev/organization-design.md §3).
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
// `write` members (docs/dev/organization-design.md §4).
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

// requireOrgRoleWrite is requireOrgRole for the handlers that change
// something: it refuses a read-only token first, then applies the role check.
//
// The order is the reason this exists rather than being left to the call
// sites. requireOrgRole deliberately does not require write scope -- reading
// the audit log with a read-only token is fine -- so each mutating handler had
// to remember to call requireWrite before it: a two-call convention repeated
// four times with nothing enforcing it, and requireUser run twice as a side
// effect every time. The order is also observable. With the role check first,
// a read-only token aimed at an organisation that does not exist would answer
// 404 where every organisation that does exist answers 403, which hands
// anyone holding one an existence oracle.
func (s *Server) requireOrgRoleWrite(w http.ResponseWriter, r *http.Request, orgName string, min Role) (*store.User, *store.Org, bool) {
	if _, ok := s.requireWrite(w, r); !ok {
		return nil, nil, false
	}
	return s.requireOrgRole(w, r, orgName, min)
}

// requireOrgRosterAccess checks the caller may see org's member list and
// answers with their effective role in it. Members always may; everyone else
// only when members_visibility is "public".
//
// The role is returned because the roster's *contents* depend on it as well:
// toOrgMemberAPI takes role >= RoleRead as its withEmail, since a public
// roster does not make the addresses on it public too.
//
// Shared by the Web UI's paged member list and the HF-compatible bare-array
// one, because a visibility rule written out twice is a rule that gets
// changed once. The HF endpoint answers the whole roster with no cursor and
// no total (see allOrgMembers), so a members-only list escaping through it
// would escape entirely rather than a page at a time.
func (s *Server) requireOrgRosterAccess(w http.ResponseWriter, r *http.Request, org *store.Org) (Role, bool) {
	ctx := r.Context()
	role, err := s.roleIn(ctx, currentUser(ctx), org.Name)
	if err != nil {
		internalError(w, "check organisation role", err)
		return RoleNone, false
	}
	if role < RoleRead && org.MembersVisibility != "public" {
		forbidden(w, "the member list of "+org.Name+" is visible to its members only")
		return RoleNone, false
	}
	return role, true
}

// ----------------------------------------------------- repository access

// loadRepoForRead fetches the repository named in the URL and enforces read
// access, writing the error response itself when it returns false. When the
// name is a former name of a repository that has since moved
// (docs/dev/repo-transfer-design.md §9), it answers according to mode instead of
// a plain 404 -- see resolveRepo and redirectMoved.
//
// "No such repository" goes out as repoNotFound rather than a bare notFound:
// huggingface_hub's hf_raise_for_status only turns a 404 into
// RepositoryNotFoundError when X-Error-Code says RepoNotFound, and that is the
// one exception HfApi.repo_exists / file_exists / revision_exists catch. Without
// the header they raise HfHubHTTPError instead of answering False, and every
// caller that probes for an optional repository or file -- transformers picking
// the next candidate filename, for one -- fails instead of moving on. The 401
// fallback in hf_raise_for_status is no help here: its REPO_API_REGEX is
// anchored on `^https://`, so a self-hosted instance served over plain HTTP
// never matches it.
func (s *Server) loadRepoForRead(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	ctx := r.Context()
	repo, err := s.resolveRepo(ctx, kind, ns, name)
	if err != nil {
		var moved *repoMovedError
		if errors.As(err, &moved) {
			if mode == redirectNone {
				// redirectNone means "answer exactly as if it never existed"
				// (see redirect.go), so it gets the same signal as a genuine
				// miss -- header included. Anything else here would leak the
				// existence of the repository at its new name through a
				// difference the message itself does not make.
				repoNotFound(w, "repository "+ns+"/"+name+" not found")
			} else {
				redirectMoved(w, r, mode, ns, name, moved)
			}
			return nil, false
		}
		if errors.Is(err, store.ErrNotFound) {
			repoNotFound(w, "repository "+ns+"/"+name+" not found")
		} else {
			internalError(w, "load repository", err)
		}
		return nil, false
	}
	return repo, true
}

// loadRepoForWrite is the gate every content-changing endpoint passes
// through: git receive-pack, the HF commit/preupload pair, the LFS upload
// batch, in-browser editing, transfers and experiment ingest. On top of the
// write permission it refuses an archived repository, so archiving one
// stops all of them in a single place (docs/dev/api-contract.md §2 "archiving").
// The two operations that must keep working on an archive -- unarchiving and
// deleting it -- use loadRepoForWriteAllowArchived instead.
func (s *Server) loadRepoForWrite(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	repo, ok := s.loadRepoForWriteAllowArchived(w, r, kind, ns, name, mode)
	if !ok {
		return nil, false
	}
	// After the permission check, so an archived repository does not answer
	// differently to someone who could not write to it anyway.
	//
	// Deliberately *not* repoNotFound: the repository exists and this caller
	// can read it. Tagging it RepoNotFound would make huggingface_hub raise
	// RepositoryNotFoundError -- and repo_exists() answer False -- for a
	// repository the very same client can still list and download.
	if repo.Archived() {
		writeError(w, http.StatusForbidden, "repository_archived",
			repo.FullName()+" is archived and read-only; unarchive it in the repository settings to make changes")
		return nil, false
	}
	return repo, true
}

// loadRepoForWriteAllowArchived enforces the write permission only. Callers
// that are not changing repository content -- delete, archive/unarchive --
// use it directly; everything else wants loadRepoForWrite.
func (s *Server) loadRepoForWriteAllowArchived(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	repo, ok := s.loadRepoForRead(w, r, kind, ns, name, mode)
	if !ok {
		return nil, false
	}
	// Also deliberately left as 401/403 rather than RepoNotFound. There is no
	// private-repository concept here (nothing in this package filters reads on
	// visibility), so a repository the caller cannot write is still one they can
	// see -- hiding it behind a 404 would only teach clients that it is gone.
	if !s.canWriteIgnoringArchive(r.Context(), repo) {
		if currentUser(r.Context()) == nil {
			unauthorized(w, "authentication required to write to "+repo.FullName())
		} else {
			forbidden(w, "you do not have write access to "+repo.FullName())
		}
		return nil, false
	}
	return repo, true
}
