// Package apperror defines typed application errors with stable, language-
// independent codes and a single source of truth for their HTTP status. Handlers
// classify failures by inspecting an *AppError via errors.As — never by matching
// substrings of a message — and surface only the sanitized public envelope while
// the cause chain stays in the logs.
package apperror

import (
	"errors"
	"net/http"
)

// Code is a stable, language-independent error identifier. Codes never change
// with the message language; clients branch on the code.
type Code string

const (
	// Request / file.
	CodeInvalidArgument      Code = "INVALID_ARGUMENT"
	CodePathOutsideWorkspace Code = "PATH_OUTSIDE_WORKSPACE"
	CodeFileNotFound         Code = "FILE_NOT_FOUND"
	CodeFileChangedOnDisk    Code = "FILE_CHANGED_ON_DISK"
	CodeRequestTooLarge      Code = "REQUEST_TOO_LARGE"
	CodeUnsupportedLanguage  Code = "UNSUPPORTED_LANGUAGE"
	CodeSourceParseFailed    Code = "SOURCE_PARSE_FAILED"

	// Symbol / retrieval.
	CodeSymbolNotFound     Code = "SYMBOL_NOT_FOUND"
	CodeSymbolAmbiguous    Code = "SYMBOL_AMBIGUOUS"
	CodeQueryRequired      Code = "QUERY_REQUIRED"
	CodeNoRelevantEvidence Code = "NO_RELEVANT_EVIDENCE"

	// State / concurrency.
	CodeAppNotReady                  Code = "APP_NOT_READY"
	CodeIndexingInProgress           Code = "INDEXING_IN_PROGRESS"
	CodeStoreVersionConflict         Code = "STORE_VERSION_CONFLICT"
	CodeDocumentVersionConflict      Code = "DOCUMENT_VERSION_CONFLICT"
	CodeDocumentAlreadyOpen          Code = "DOCUMENT_ALREADY_OPEN"
	CodeDocumentDirty                Code = "DOCUMENT_DIRTY"
	CodeDocumentNotFound             Code = "DOCUMENT_NOT_FOUND"
	CodeDocumentConflictChanged      Code = "DOCUMENT_CONFLICT_CHANGED"
	CodeDocumentConflictNotFound     Code = "DOCUMENT_CONFLICT_NOT_FOUND"
	CodeDocumentResolutionInProgress Code = "DOCUMENT_RESOLUTION_IN_PROGRESS"
	CodeOverlayLimitExceeded         Code = "OVERLAY_LIMIT_EXCEEDED"
	CodeDeepWikiInProgress           Code = "DEEPWIKI_GENERATION_IN_PROGRESS"
	CodeJobQueueFull                 Code = "JOB_QUEUE_FULL"
	CodeJobNotFound                  Code = "JOB_NOT_FOUND"
	CodeJobRevisionConflict          Code = "JOB_REVISION_CONFLICT"
	CodeJobTypeUnsupported           Code = "JOB_TYPE_UNSUPPORTED"
	CodeJobResultUnavailable         Code = "JOB_RESULT_UNAVAILABLE"

	// Infrastructure.
	CodeProviderUnavailable  Code = "PROVIDER_UNAVAILABLE"
	CodeProviderTimeout      Code = "PROVIDER_TIMEOUT"
	CodeProviderUnauthorized Code = "PROVIDER_UNAUTHORIZED"
	CodeModelOutputInvalid   Code = "MODEL_OUTPUT_INVALID"
	CodeEmbeddingUnavailable Code = "EMBEDDING_UNAVAILABLE"
	CodeStoreCorrupted       Code = "STORE_CORRUPTED"
	CodePersistenceFailed    Code = "PERSISTENCE_FAILED"
	CodeInternalError        Code = "INTERNAL_ERROR"
	CodeRequestCanceled      Code = "REQUEST_CANCELLED"

	// SQLite persistence (P8).
	CodeDatabaseOpenFailed                Code = "DATABASE_OPEN_FAILED"
	CodeDatabaseMigrationFailed           Code = "DATABASE_MIGRATION_FAILED"
	CodeDatabaseMigrationChecksumMismatch Code = "DATABASE_MIGRATION_CHECKSUM_MISMATCH"
	CodeDatabaseVersionTooNew             Code = "DATABASE_VERSION_TOO_NEW"
	CodeArtifactInputSnapshotStale        Code = "ARTIFACT_INPUT_SNAPSHOT_STALE"
	CodeArtifactNotFound                  Code = "ARTIFACT_NOT_FOUND"
)

// statusRequestCanceled is the non-standard client-closed-request status used by
// nginx and others; the request never completed, so no envelope reaches the peer.
const statusRequestCanceled = 499

// statusByCode is the single mapping from a code to its HTTP status. An unknown
// code defaults to 500 via HTTPStatus.
var statusByCode = map[Code]int{
	CodeInvalidArgument:              http.StatusBadRequest,            // 400
	CodePathOutsideWorkspace:         http.StatusBadRequest,            // 400
	CodeFileNotFound:                 http.StatusNotFound,              // 404
	CodeFileChangedOnDisk:            http.StatusConflict,              // 409
	CodeRequestTooLarge:              http.StatusRequestEntityTooLarge, // 413
	CodeUnsupportedLanguage:          http.StatusUnprocessableEntity,   // 422
	CodeSourceParseFailed:            http.StatusUnprocessableEntity,   // 422
	CodeSymbolNotFound:               http.StatusNotFound,              // 404
	CodeSymbolAmbiguous:              http.StatusUnprocessableEntity,   // 422 (semantic, not a state conflict)
	CodeQueryRequired:                http.StatusBadRequest,            // 400
	CodeNoRelevantEvidence:           http.StatusUnprocessableEntity,   // 422
	CodeAppNotReady:                  http.StatusServiceUnavailable,    // 503
	CodeIndexingInProgress:           http.StatusConflict,              // 409
	CodeStoreVersionConflict:         http.StatusConflict,              // 409
	CodeDocumentVersionConflict:      http.StatusConflict,              // 409
	CodeDocumentAlreadyOpen:          http.StatusConflict,              // 409
	CodeDocumentDirty:                http.StatusConflict,              // 409
	CodeDocumentNotFound:             http.StatusNotFound,              // 404
	CodeDocumentConflictChanged:      http.StatusConflict,              // 409
	CodeDocumentConflictNotFound:     http.StatusNotFound,              // 404
	CodeDocumentResolutionInProgress: http.StatusConflict,              // 409
	CodeOverlayLimitExceeded:         http.StatusTooManyRequests,       // 429
	CodeDeepWikiInProgress:           http.StatusConflict,              // 409
	CodeJobQueueFull:                 http.StatusTooManyRequests,       // 429
	CodeJobNotFound:                  http.StatusNotFound,              // 404
	CodeJobRevisionConflict:          http.StatusConflict,              // 409
	CodeJobTypeUnsupported:           http.StatusBadRequest,            // 400
	CodeJobResultUnavailable:         http.StatusConflict,              // 409
	CodeModelOutputInvalid:           http.StatusBadGateway,            // 502 (upstream model produced ungrounded output)
	CodeProviderUnavailable:          http.StatusServiceUnavailable,    // 503
	CodeProviderTimeout:              http.StatusGatewayTimeout,        // 504
	CodeProviderUnauthorized:         http.StatusServiceUnavailable,    // 503 (local client; no credential leak)
	CodeEmbeddingUnavailable:         http.StatusServiceUnavailable,    // 503
	CodeStoreCorrupted:               http.StatusServiceUnavailable,    // 503
	CodePersistenceFailed:            http.StatusServiceUnavailable,    // 503 (treated as potentially recoverable)
	CodeInternalError:                http.StatusInternalServerError,   // 500
	CodeRequestCanceled:              statusRequestCanceled,            // 499
	// SQLite persistence: infrastructure/state-integrity failures.
	CodeDatabaseOpenFailed:                http.StatusServiceUnavailable, // 503
	CodeDatabaseMigrationFailed:           http.StatusServiceUnavailable, // 503
	CodeDatabaseMigrationChecksumMismatch: http.StatusServiceUnavailable, // 503 (incompatible/inconsistent on-disk schema)
	CodeDatabaseVersionTooNew:             http.StatusServiceUnavailable, // 503 (DB written by a newer build)
	CodeArtifactInputSnapshotStale:        http.StatusConflict,           // 409 (snapshot moved during generation)
	CodeArtifactNotFound:                  http.StatusNotFound,           // 404
}

// AppError is a typed application error. Message is the sanitized, public English
// text; Cause is preserved for logging but never serialized. Details is
// allowlisted per code by the constructors and carries no secrets or source
// content.
type AppError struct {
	Code      Code
	Message   string
	Retryable bool
	Details   map[string]any
	Cause     error
}

// Error returns the public message only — never the cause chain — so a stray
// fmt-print of the error cannot leak internal detail to a client.
func (e *AppError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

// Unwrap exposes the cause for errors.Is/errors.As traversal without surfacing it
// to clients.
func (e *AppError) Unwrap() error { return e.Cause }

// Is matches by code, so errors.Is(err, &AppError{Code: ...}) holds across wraps.
func (e *AppError) Is(target error) bool {
	var other *AppError
	if errors.As(target, &other) {
		return e.Code == other.Code
	}
	return false
}

// HTTPStatus returns the HTTP status for any error: the mapped status of the
// first *AppError in the chain, or 500 for an untyped or unknown error.
func HTTPStatus(err error) int {
	var appErr *AppError
	if errors.As(err, &appErr) {
		if status, ok := statusByCode[appErr.Code]; ok {
			return status
		}
	}
	return http.StatusInternalServerError
}

// As extracts the first *AppError in the chain, if any.
func As(err error) (*AppError, bool) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

// newAppError is the single internal builder used by every constructor; it keeps
// Details allowlisted (only constructors pass keys) and the cause private.
func newAppError(code Code, message string, retryable bool, details map[string]any, cause error) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable, Details: details, Cause: cause}
}
