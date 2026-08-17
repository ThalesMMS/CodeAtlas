package domain

// PublishedWorkspaceChange is emitted only after a reconcile/commit has
// published the store snapshot. Raw filesystem events never use this contract.
type PublishedWorkspaceChange struct {
	OperationID string                `json:"operationId"`
	SnapshotID  SnapshotID            `json:"snapshotId"`
	Revision    Revision              `json:"revision"`
	Changes     []PublishedFileChange `json:"changes"`
}

// PublishedFileChange describes one file's final-state change in a published
// workspace snapshot.
type PublishedFileChange struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	OldHash string `json:"oldHash,omitempty"`
	NewHash string `json:"newHash,omitempty"`
	Origin  string `json:"origin"`
}

const (
	FileChangeAdded             = "added"
	FileChangeModified          = "modified"
	FileChangeRemoved           = "removed"
	FileChangeBecameIgnored     = "became_ignored"
	FileChangeBecameUnsupported = "became_unsupported"

	FileChangeOriginExternal = "external"
	FileChangeOriginInternal = "internal"
)
