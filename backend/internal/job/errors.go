package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

var (
	ErrQueueFull         = errors.New("job queue full")
	ErrNotFound          = errors.New("job not found")
	ErrRevisionConflict  = errors.New("job revision conflict")
	ErrStopped           = errors.New("job manager stopped")
	ErrInvalidSpec       = errors.New("invalid job spec")
	ErrStale             = errors.New("job input stale")
	ErrResultUnavailable = errors.New("job result unavailable")
)

func HTTPError(err error, id domain.JobID) error {
	switch {
	case errors.Is(err, ErrQueueFull):
		return apperror.JobQueueFull()
	case errors.Is(err, ErrNotFound):
		return apperror.JobNotFound(string(id))
	case errors.Is(err, ErrRevisionConflict):
		return apperror.JobRevisionConflict(string(id))
	case errors.Is(err, ErrResultUnavailable):
		return apperror.JobResultUnavailable(string(id))
	case errors.Is(err, ErrStopped):
		return apperror.AppNotReady("jobs shutting down")
	case errors.Is(err, ErrInvalidSpec):
		return apperror.InvalidArgument("job", err)
	default:
		return err
	}
}

func publicError(err error) *domain.PublicError {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrStale), errors.Is(err, apperror.ArtifactInputSnapshotStale()):
		return &domain.PublicError{Code: "JOB_INPUT_STALE", Message: "The job input became stale."}
	case errors.Is(err, context.Canceled):
		return &domain.PublicError{Code: "JOB_CANCELED", Message: "Job canceled."}
	case errors.Is(err, context.DeadlineExceeded):
		return &domain.PublicError{Code: "JOB_TIMEOUT", Message: "Job timed out."}
	}
	if appErr, ok := apperror.As(err); ok {
		return &domain.PublicError{Code: string(appErr.Code), Message: appErr.Message}
	}
	return &domain.PublicError{Code: "JOB_FAILED", Message: "Failed to run job."}
}

func panicError(value any) error {
	return fmt.Errorf("job panic: %w", panicSentinel{value: value})
}

type panicSentinel struct{ value any }

func (p panicSentinel) Error() string { return "panic recovered" }

func isPanicError(err error) bool {
	var sentinel panicSentinel
	return errors.As(err, &sentinel)
}
