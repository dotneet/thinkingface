package apitypes

import "time"

// ------------------------------------------------------- site administration

// PasswordChangeRequest is the body of PATCH /api/v1/me/password. The current
// password is always required: holding a session is not on its own permission
// to replace the credential that session was minted from.
type PasswordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// AdminUser is one account as GET /api/v1/admin/users lists it. The stored
// password hash has no field here and never will: this type *is* the wire
// contract, so a field that does not exist cannot be serialised by accident.
type AdminUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	// IsAdmin is the instance-wide administrator flag (users.is_admin), not
	// a role in any organisation.
	IsAdmin bool `json:"is_admin"`
	// Disabled reports whether the account is suspended. A disabled account
	// authenticates on no path at all -- not password, not access token, not
	// SSH key -- which is what makes it the offboarding switch. Resetting a
	// password deliberately does not revoke tokens, so before this existed
	// there was no way to actually cut somebody off.
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	// LastLoginAt is when this account last authenticated with its password
	// (a session being minted), null for one that never has. Access tokens
	// and SSH keys carry their own last-used timestamps and deliberately do
	// not move this one: the question it answers is "is anybody still using
	// this account", for which an automation's token is the wrong signal.
	LastLoginAt *time.Time `json:"last_login_at" tstype:"string | null,required"`
	// Approval is "pending" for an account that signed up while
	// TF_SIGNUP_REQUIRE_APPROVAL was on and has not been approved yet. A
	// pending account cannot authenticate on any path.
	Approval UserApproval `json:"approval"`
}

// UserApproval is whether a self-registered account has been let in yet.
type UserApproval string

const (
	UserApprovalApproved UserApproval = "approved"
	UserApprovalPending  UserApproval = "pending"
)

// AdminUserListResponse is one page of the account directory. Total counts
// every account matching `search`, ignoring the page window.
type AdminUserListResponse struct {
	Items []AdminUser `json:"items"`
	Total int64       `json:"total"`
}

// AdminUserResponse wraps the account after an administrative change.
type AdminUserResponse struct {
	User AdminUser `json:"user"`
}

// AdminUserCreateRequest is the body of POST /api/v1/admin/users: a site
// administrator adds an account directly. It is the only way to create one on
// an instance with TF_ALLOW_SIGNUP=false, so it deliberately does not consult
// that setting.
type AdminUserCreateRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// IsAdmin makes the new account a site administrator. Optional; the
	// account is an ordinary user when it is absent or false.
	IsAdmin bool `json:"is_admin,omitempty"`
}

// AdminUserUpdateRequest is the body of PATCH
// /api/v1/admin/users/{username}. Both fields are optional and an absent one
// is left unchanged, but a body setting neither is refused (400) rather than
// treated as a no-op.
type AdminUserUpdateRequest struct {
	// Password replaces the account's password and revokes its sessions.
	// The account's access tokens are deliberately not revoked.
	Password *string `json:"password,omitempty"`
	// IsAdmin grants or revokes site administrator rights. Revoking your
	// own is 400; revoking the last one on the instance is 409.
	IsAdmin *bool `json:"is_admin,omitempty"`
	// Disabled suspends or restores the account. Suspending it stops every
	// identity path at once (session, password, access token, SSH key) and
	// revokes its sessions; disabling your own account is 400
	// (self_disable) and disabling the last usable site administrator is
	// 409 (last_admin). Restoring does not bring back credentials revoked
	// separately.
	Disabled *bool `json:"disabled,omitempty"`
	// Approval admits a pending self-registration ("approved") or puts an
	// account back in the waiting room ("pending"). Sending "pending" for
	// your own account is 400 (self_pending); doing it to the last usable
	// site administrator is 409 (last_admin), the same pair of codes the
	// Disabled field uses.
	Approval *UserApproval `json:"approval,omitempty"`
}

// AdminNamespaceUsage is one namespace as GET /api/v1/admin/namespaces lists
// it: what it is storing and what it is allowed to store.
type AdminNamespaceUsage struct {
	Namespace string        `json:"namespace"`
	Kind      NamespaceKind `json:"kind"`
	LFSSize   int64         `json:"lfs_size"`
	NumRepos  int64         `json:"num_repos"`
	// QuotaBytes is this namespace's own override; null means it has none
	// and the instance default applies.
	QuotaBytes *int64 `json:"quota_bytes" tstype:"number | null,required"`
	// EffectiveQuotaBytes is what is actually enforced on an upload: the
	// override when set, otherwise the instance default. Null is unlimited.
	EffectiveQuotaBytes *int64 `json:"effective_quota_bytes" tstype:"number | null,required"`
}

// AdminNamespaceListResponse is one page of the namespace directory.
type AdminNamespaceListResponse struct {
	Items []AdminNamespaceUsage `json:"items"`
	Total int64                 `json:"total"`
	// DefaultQuotaBytes is the instance-wide default every namespace without
	// an override gets (TF_DEFAULT_STORAGE_QUOTA_BYTES). Null is unlimited.
	// It is configuration, not data: changing it needs a redeploy.
	DefaultQuotaBytes *int64 `json:"default_quota_bytes" tstype:"number | null,required"`
}

// AdminNamespaceQuotaRequest is the body of PATCH
// /api/v1/admin/namespaces/{ns}. The field is required and nullable: null
// clears the override so the instance default applies again, which is a
// different thing from setting a quota of zero.
type AdminNamespaceQuotaRequest struct {
	QuotaBytes *int64 `json:"quota_bytes" tstype:"number | null,required"`
}

// SyncJob is one row of the post-push queue as GET /api/v1/admin/sync-jobs
// lists it. Only jobs that exhausted their attempts are listed: a job still
// retrying is not an operator's problem yet, and the queue is otherwise high
// churn.
//
// A failed job means the repository's file index, search entry and blobs/
// export are frozen at the previous push. Nothing republishes it on its own,
// which is why this listing exists at all -- before it, the only trace was a
// single log line.
type SyncJob struct {
	ID int64 `json:"id"`
	// Repo is the full name including the kind segment, e.g.
	// "datasets/acme/imdb-ja", so an operator can open it directly.
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	// Attempts is how many times the job was claimed before it parked.
	Attempts int `json:"attempts"`
	// LastError is the error from the final attempt, verbatim.
	LastError string    `json:"last_error"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SyncJobListResponse is one page of failed sync jobs. Total counts every
// failed job, ignoring the page window.
type SyncJobListResponse struct {
	Items []SyncJob `json:"items"`
	Total int64     `json:"total"`
}
