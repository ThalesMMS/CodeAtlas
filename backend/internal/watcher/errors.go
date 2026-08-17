package watcher

const (
	ErrCodeCreateFailed         = "WATCHER_CREATE_FAILED"
	ErrCodeAddFailed            = "WATCHER_ADD_FAILED"
	ErrCodeEventChannelClosed   = "WATCHER_EVENT_CHANNEL_CLOSED"
	ErrCodeErrorChannelClosed   = "WATCHER_ERROR_CHANNEL_CLOSED"
	ErrCodeBackpressureOverflow = "WATCHER_BACKPRESSURE_OVERFLOW"
	ErrCodePathOutsideWorkspace = "WATCHER_PATH_OUTSIDE_WORKSPACE"
	ErrCodeDesynchronized       = "WATCHER_DESYNCHRONIZED"
	ErrCodeClosed               = "WATCHER_CLOSED"
)

type WatcherError struct {
	Code    string
	Message string
	Cause   error
}

func (e *WatcherError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

func (e *WatcherError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func watcherError(code, message string, cause error) *WatcherError {
	return &WatcherError{Code: code, Message: message, Cause: cause}
}

func addFailedError(relative string, cause error) *WatcherError {
	return watcherError(ErrCodeAddFailed, "failed to watch "+relative, cause)
}
