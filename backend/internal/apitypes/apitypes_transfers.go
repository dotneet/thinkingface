package apitypes

import "time"

// ------------------------------------------------------------- transfers

// RepoTransferStatus is the lifecycle state of a transfer request
// (docs/dev/repo-transfer-design.md §7).
type RepoTransferStatus string

const (
	RepoTransferPending   RepoTransferStatus = "pending"
	RepoTransferAccepted  RepoTransferStatus = "accepted"
	RepoTransferRejected  RepoTransferStatus = "rejected"
	RepoTransferCancelled RepoTransferStatus = "cancelled"
	RepoTransferExpired   RepoTransferStatus = "expired"
)

// RepoTransferRequest asks to move a repository to another namespace (and
// optionally rename it at the same time).
type RepoTransferRequest struct {
	// Namespace is the destination user or organisation.
	Namespace string `json:"namespace"`
	// Name is the new repository name; empty keeps the current one.
	Name string `json:"name,omitempty"`
}

// RepoTransfer is one transfer request as the web UI sees it.
type RepoTransfer struct {
	ID            int64              `json:"id"`
	Kind          RepoKind           `json:"kind"`
	FromNamespace string             `json:"from_namespace"`
	FromName      string             `json:"from_name"`
	ToNamespace   string             `json:"to_namespace"`
	ToName        string             `json:"to_name"`
	RequestedBy   string             `json:"requested_by"`
	Status        RepoTransferStatus `json:"status"`
	ExpiresAt     time.Time          `json:"expires_at"`
	CreatedAt     time.Time          `json:"created_at"`
}

// RepoTransferResponse answers a transfer call. Repo is present only when the
// move completed (immediately, or on accept) and describes the repository at
// its new location.
type RepoTransferResponse struct {
	Transfer RepoTransfer `json:"transfer"`
	Repo     *RepoDetail  `json:"repo,omitempty"`
}

// MyTransfersResponse lists the pending transfers the signed-in user can act
// on: Incoming ones they may accept or reject, Outgoing ones they may cancel.
type MyTransfersResponse struct {
	Incoming []RepoTransfer `json:"incoming"`
	Outgoing []RepoTransfer `json:"outgoing"`
}
