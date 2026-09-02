package apitypes

import "time"

// ------------------------------------------------------- accounts and auth

// Namespace is somewhere the user may create repositories.
type Namespace struct {
	Name string        `json:"name"`
	Kind NamespaceKind `json:"kind"`
	// Role is the user's role in this namespace ("admin", "write", ...), or
	// "" when membership carries no explicit role.
	Role string `json:"role"`
}

// User is the signed-in account as the web UI sees it.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"is_admin"`
	// DisplayName and AvatarURL come from the user's own namespace row
	// (docs/dev/namespace-design.md §5.3); both may be "".
	DisplayName string      `json:"display_name"`
	AvatarURL   string      `json:"avatar_url"`
	Namespaces  []Namespace `json:"namespaces"`
}

// UserResponse wraps the account in the envelope /me, /login and /signup use.
type UserResponse struct {
	User User `json:"user"`
}

// ------------------------------------------------------------- namespaces

// NamespaceProfile is the public face of a namespace -- a user or an
// organisation -- as GET /api/v1/namespaces/{ns} returns it
// (docs/dev/namespace-design.md §7.1). Both kinds share the same profile
// columns on the namespaces row; the organisation-only fields are zero for
// a user namespace.
type NamespaceProfile struct {
	// Name is the canonical spelling. Namespace names are case-insensitive,
	// so a lookup for "Alice" answers with Name "alice" when that is how the
	// account was registered; the UI redirects to the canonical URL.
	Name        string        `json:"name"`
	Kind        NamespaceKind `json:"kind"`
	DisplayName string        `json:"display_name"`
	Description string        `json:"description"`
	Website     string        `json:"website"`
	AvatarURL   string        `json:"avatar_url"`
	CreatedAt   time.Time     `json:"created_at"`

	NumModels      int64 `json:"num_models"`
	NumDatasets    int64 `json:"num_datasets"`
	NumExperiments int64 `json:"num_experiments"`

	// NumMembers and MembersVisibility only mean something for an
	// organisation; a user namespace reports 0 and "".
	NumMembers        int64             `json:"num_members"`
	MembersVisibility MembersVisibility `json:"members_visibility"`

	// ViewerRole is the caller's effective role (docs/dev/organization-design.md
	// §3.1): "admin" for the owner of a user namespace and for a site admin,
	// the org_members role for an organisation, "" otherwise.
	ViewerRole OrgRole `json:"viewer_role"`
	// CanEdit is ViewerRole == "admin", spelled out so the UI can show the
	// "Edit profile" / "Settings" button without re-deriving it.
	CanEdit bool `json:"can_edit"`
}

// NamespaceResponse wraps one profile.
type NamespaceResponse struct {
	Namespace NamespaceProfile `json:"namespace"`
}

// NamespaceProfileUpdate is the body of PATCH /api/v1/me/profile. Every
// field is optional; a present field replaces the stored value (an empty
// string clears it). The namespace name itself is not editable
// (docs/dev/namespace-design.md §5.4).
type NamespaceProfileUpdate struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	Website     *string `json:"website,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// TokenItem is one API token, without its secret value.
type TokenItem struct {
	ID    int64      `json:"id"`
	Name  string     `json:"name"`
	Scope TokenScope `json:"scope"`
	// CreatedAt is an RFC 3339 timestamp.
	CreatedAt time.Time `json:"created_at"`
	// LastUsedAt is null until the token authenticates a request.
	LastUsedAt *time.Time `json:"last_used_at" tstype:"string | null,required"`
	// ExpiresAt is null for a token that never expires.
	ExpiresAt *time.Time `json:"expires_at" tstype:"string | null,required"`
}

// TokenListResponse is the body of GET /api/v1/tokens.
type TokenListResponse struct {
	Items []TokenItem `json:"items"`
}

// CreateTokenResponse returns the freshly minted token. The plaintext value
// appears here and nowhere else, so a client that loses it must issue another.
type CreateTokenResponse struct {
	TokenItem `tstype:",extends"`
	Token     string `json:"token"`
}

// SSHKeyItem is one registered SSH public key. Unlike a token there is no
// secret to withhold: the key material is public by construction, so it is
// returned in full and the UI can show it.
type SSHKeyItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// KeyType is the algorithm name, e.g. "ssh-ed25519".
	KeyType string `json:"key_type"`
	// PublicKey is the canonical "<type> <base64>" authorized_keys line,
	// with the comment stripped.
	PublicKey string `json:"public_key"`
	// Fingerprint is the OpenSSH "SHA256:<base64>" form, which is what
	// `ssh-keygen -lf` prints.
	Fingerprint string `json:"fingerprint"`
	// CreatedAt is an RFC 3339 timestamp.
	CreatedAt time.Time `json:"created_at"`
	// LastUsedAt is null until the key authenticates an SSH session.
	LastUsedAt *time.Time `json:"last_used_at" tstype:"string | null,required"`
}

// SSHKeyListResponse is the body of GET /api/v1/me/ssh-keys.
type SSHKeyListResponse struct {
	Items []SSHKeyItem `json:"items"`
}
