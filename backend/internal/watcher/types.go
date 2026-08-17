package watcher

import (
	"context"
	"strings"
	"time"
)

type WatchOperation uint32

const (
	WatchCreate WatchOperation = 1 << iota
	WatchWrite
	WatchRemove
	WatchRename
	WatchMetadata
	WatchRescanRequired
)

func (op WatchOperation) Has(flag WatchOperation) bool { return op&flag != 0 }

func (op WatchOperation) String() string {
	if op == 0 {
		return "none"
	}
	parts := make([]string, 0, 6)
	for _, item := range []struct {
		flag WatchOperation
		name string
	}{
		{WatchCreate, "create"},
		{WatchWrite, "write"},
		{WatchRemove, "remove"},
		{WatchRename, "rename"},
		{WatchMetadata, "metadata"},
		{WatchRescanRequired, "rescan_required"},
	} {
		if op.Has(item.flag) {
			parts = append(parts, item.name)
		}
	}
	return strings.Join(parts, "|")
}

const (
	KindFile      = "file"
	KindDirectory = "directory"
	KindUnknown   = "unknown"
	KindSymlink   = "symlink"

	SourceFSNotify        = "fsnotify"
	SourceWatcherInternal = "watcher-internal"
)

type WorkspaceEvent struct {
	Sequence   uint64         `json:"sequence"`
	Path       string         `json:"path"`
	Operations WatchOperation `json:"operations"`
	Kind       string         `json:"kind"`
	ObservedAt time.Time      `json:"observedAt"`
	Source     string         `json:"source"`
	ReasonCode string         `json:"reasonCode,omitempty"`
}

type WatcherState string

const (
	StateNew            WatcherState = "new"
	StateStarting       WatcherState = "starting"
	StateRunning        WatcherState = "running"
	StateDesynchronized WatcherState = "desynchronized"
	StateFailed         WatcherState = "failed"
	StateClosing        WatcherState = "closing"
	StateClosed         WatcherState = "closed"
)

type WatcherSnapshot struct {
	State                 WatcherState      `json:"state"`
	WatchedDirs           []string          `json:"watchedDirs"`
	WatchedDirectoryCount int               `json:"watchedDirectoryCount"`
	CurrentSequence       uint64            `json:"currentSequence"`
	EventsReceived        uint64            `json:"eventsReceived"`
	ErrorsReceived        uint64            `json:"errorsReceived"`
	OverflowCount         uint64            `json:"overflowCount"`
	DesyncCount           uint64            `json:"desyncCount"`
	LastError             string            `json:"lastError,omitempty"`
	StartedAt             time.Time         `json:"startedAt,omitempty"`
	LastEventAt           time.Time         `json:"lastEventAt,omitempty"`
	OperationCounts       map[string]uint64 `json:"operationCounts,omitempty"`
}

type WorkspaceWatcher interface {
	Start(ctx context.Context) error
	Events() <-chan WorkspaceEvent
	Errors() <-chan error
	Snapshot() WatcherSnapshot
	AcknowledgeResync()
	Close() error
}
