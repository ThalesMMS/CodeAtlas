package scheduler

import (
	"context"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

const (
	ModeAuto    = "auto"
	ModeNative  = "native"
	ModePolling = "polling"

	ReasonDebounce      = "debounce"
	ReasonMaxDelay      = "max_delay"
	ReasonManual        = "manual"
	ReasonPolling       = "polling"
	ReasonPeriodic      = "periodic"
	ReasonDirectoryHint = "directory_hint"
	ReasonIgnoreChange  = "ignore_change"
	ReasonWatcherDesync = "watcher_desync"
)

type Timer interface {
	Stop() bool
	Reset(time.Duration) bool
}

type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}

type realClock struct{}

func RealClock() Clock { return realClock{} }

func (realClock) Now() time.Time { return time.Now().UTC() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

type ReconcileExecutor interface {
	CurrentRevision() uint64
	ReconcilePaths(ctx context.Context, paths []string, expectedRevision uint64) error
	ReconcileSubtree(ctx context.Context, root string, expectedRevision uint64) error
	ReconcileFull(ctx context.Context, expectedRevision uint64) error
}

type ManualReconciler interface {
	RequestFull(ctx context.Context, reason string) error
	State() domain.SchedulerState
}
