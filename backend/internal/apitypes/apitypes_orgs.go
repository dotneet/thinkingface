package apitypes

import "time"

// --------------------------------------------------------- organisations
// (docs/dev/organization-design.md)

// OrgRole is a member's role in an organisation. "" means "not a member".
type OrgRole string

const (
	OrgRoleAdmin OrgRole = "admin"
	OrgRoleWrite OrgRole = "write"
	OrgRoleRead  OrgRole = "read"
)

// MembersVisibility says who may list an organisation's members.
type MembersVisibility string

const (
	MembersVisibilityMembers MembersVisibility = "members"
	MembersVisibilityPublic  MembersVisibility = "public"
)

// Org is one organisation as the web UI sees it.
type Org struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Website     string `json:"website"`
	AvatarURL   string `json:"avatar_url"`

	// MembersVisibility is about the member list, not about repositories:
	// there is no repository visibility concept here
	// (docs/dev/content-addressed-storage-design.md §1).
	MembersVisibility MembersVisibility `json:"members_visibility"`

	NumMembers int64     `json:"num_members"`
	NumRepos   int64     `json:"num_repos"`
	CreatedAt  time.Time `json:"created_at"`
	// ViewerRole is the caller's effective role ("admin" for a site admin,
	// "" when signed out or not a member).
	ViewerRole OrgRole `json:"viewer_role"`
}

// OrgMember is one membership row.
type OrgMember struct {
	Username string `json:"username"`
	// Email is "" when the member list is being viewed by a non-member
	// (members_visibility = "public").
	Email     string    `json:"email"`
	Role      OrgRole   `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// OrgAuditEntry is one line of an organisation's audit log.
type OrgAuditEntry struct {
	ID int64 `json:"id"`
	// Actor is the username that performed the action ("" when the account
	// has since been deleted and nothing was recorded).
	Actor  string `json:"actor"`
	Action string `json:"action"`
	// Target is the affected username, repository full name, or webhook URL,
	// depending on Action.
	Target    string         `json:"target"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

// OrgCreateRequest is the body of POST /api/v1/orgs.
type OrgCreateRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// OrgUpdateRequest is the body of PATCH /api/v1/orgs/{org}. Every field is
// optional; absent ones are left unchanged.
type OrgUpdateRequest struct {
	DisplayName       *string            `json:"display_name,omitempty"`
	Description       *string            `json:"description,omitempty"`
	Website           *string            `json:"website,omitempty"`
	AvatarURL         *string            `json:"avatar_url,omitempty"`
	MembersVisibility *MembersVisibility `json:"members_visibility,omitempty"`
}

// OrgMemberAddRequest is the body of POST /api/v1/orgs/{org}/members.
type OrgMemberAddRequest struct {
	Username string `json:"username"`
	// Role defaults to "read" when empty.
	Role OrgRole `json:"role,omitempty"`
}

// OrgMemberUpdateRequest is the body of PATCH /api/v1/orgs/{org}/members/{username}.
type OrgMemberUpdateRequest struct {
	Role OrgRole `json:"role"`
}

// OrgResponse wraps one organisation.
type OrgResponse struct {
	Org Org `json:"org"`
}

// OrgListResponse is the body of GET /api/v1/orgs and GET /api/v1/me/orgs.
type OrgListResponse struct {
	Items []Org `json:"items"`
	Total int64 `json:"total"`
}

// OrgMembersResponse is one page of GET /api/v1/orgs/{org}/members. Total is
// the organisation's whole membership, ignoring the page window, so a client
// can tell a full page with more behind it from the end of the roster.
type OrgMembersResponse struct {
	Items []OrgMember `json:"items"`
	Total int64       `json:"total"`
}

// OrgMemberResponse wraps one membership row.
type OrgMemberResponse struct {
	Member OrgMember `json:"member"`
}

// OrgAuditLogResponse is one page of GET /api/v1/orgs/{org}/audit-log.
// NextBefore is the cursor for the following page, 0 when this was the last.
type OrgAuditLogResponse struct {
	Items      []OrgAuditEntry `json:"items"`
	NextBefore int64           `json:"next_before"`
}
