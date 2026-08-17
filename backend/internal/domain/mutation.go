package domain

import "time"

// InternalMutationID identifies one CodeAtlas-authored source mutation while it
// moves from staged save to observed filesystem state.
type InternalMutationID string

// MutationState is the in-memory lifecycle of an internal mutation.
type MutationState string

const (
	MutationStaged    MutationState = "staged"
	MutationPublished MutationState = "published"
	MutationObserved  MutationState = "observed"
	MutationExpired   MutationState = "expired"
)

// InternalMutation records enough provenance to distinguish a CodeAtlas save
// from an external edit when the final-state reconciler observes the path.
type InternalMutation struct {
	ID                   InternalMutationID `json:"id"`
	TransactionID        string             `json:"transactionId,omitempty"`
	Path                 string             `json:"path"`
	PreviousContentHash  string             `json:"previousContentHash,omitempty"`
	PublishedContentHash string             `json:"publishedContentHash,omitempty"`
	PublishedSnapshotID  SnapshotID         `json:"publishedSnapshotId,omitempty"`
	PublishedRevision    Revision           `json:"publishedRevision,omitempty"`
	RegisteredAt         time.Time          `json:"registeredAt,omitempty"`
	PublishedAt          time.Time          `json:"publishedAt,omitempty"`
	ExpiresAt            time.Time          `json:"expiresAt,omitempty"`
	ObservedAt           time.Time          `json:"observedAt,omitempty"`
	ObservedSequence     uint64             `json:"observedSequence,omitempty"`
	State                MutationState      `json:"state"`
}

// FileObservation is a reconciler's final-state read of one path. It is based
// on current disk and active store state, not on raw filesystem event payloads.
type FileObservation struct {
	Path             string    `json:"path"`
	Exists           bool      `json:"exists"`
	ContentHash      string    `json:"contentHash,omitempty"`
	StoreContentHash string    `json:"storeContentHash,omitempty"`
	StoreRevision    Revision  `json:"storeRevision,omitempty"`
	Identity         string    `json:"identity,omitempty"`
	HintSequenceMin  uint64    `json:"hintSequenceMin,omitempty"`
	HintSequenceMax  uint64    `json:"hintSequenceMax,omitempty"`
	ObservedAt       time.Time `json:"observedAt,omitempty"`
}

// ObservationClassification describes how a final-state observation relates to
// known repository state and any pending internal mutation.
type ObservationClassification string

const (
	ObservationUnchanged          ObservationClassification = "unchanged"
	ObservationSelfWriteConfirmed ObservationClassification = "self_write_confirmed"
	ObservationExternalChange     ObservationClassification = "external_change"
	ObservationRemoved            ObservationClassification = "removed"
	ObservationBecameIgnored      ObservationClassification = "became_ignored"
	ObservationError              ObservationClassification = "error"
)

// MutationMatch is the registry's classification of a FileObservation.
type MutationMatch struct {
	Matched        bool                      `json:"matched"`
	MutationID     InternalMutationID        `json:"mutationId,omitempty"`
	Classification ObservationClassification `json:"classification"`
}

// MutationRegistrySnapshot is an immutable status/metrics view of the registry.
type MutationRegistrySnapshot struct {
	StagedCount                int                `json:"stagedCount"`
	PublishedCount             int                `json:"publishedCount"`
	ObservedCount              int                `json:"observedCount"`
	ExpiredCount               int                `json:"expiredCount"`
	PathsTracked               int                `json:"pathsTracked"`
	SelfWriteConfirmedTotal    uint64             `json:"selfWriteConfirmedTotal"`
	ExternalAfterInternalTotal uint64             `json:"externalAfterInternalTotal"`
	Entries                    []InternalMutation `json:"entries,omitempty"`
}
