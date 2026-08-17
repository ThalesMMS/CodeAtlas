package apperror

// The constructors below are the only way to build an AppError, so Details is
// always allowlisted per code and the public messages stay consistent and in
// English. Each accepts only the specific, typed detail parameters its code allows.

// ---- Request / file ----

// InvalidArgument reports a malformed request argument. field names the offending
// input ("path", "body", ...); cause is logged, never shown.
func InvalidArgument(field string, cause error) *AppError {
	details := map[string]any{}
	if field != "" {
		details["field"] = field
	}
	return newAppError(CodeInvalidArgument, "Invalid request.", false, details, cause)
}

// InvalidArgumentMessage is InvalidArgument with a specific public message.
func InvalidArgumentMessage(field, message string, cause error) *AppError {
	err := InvalidArgument(field, cause)
	err.Message = message
	return err
}

func PathOutsideWorkspace(path string) *AppError {
	return newAppError(CodePathOutsideWorkspace, "The path is outside the workspace.", false,
		map[string]any{"path": path}, nil)
}

func FileNotFound(path string, cause error) *AppError {
	return newAppError(CodeFileNotFound, "File not found.", false,
		map[string]any{"path": path}, cause)
}

func FileChangedOnDisk(path, currentContentHash string) *AppError {
	return newAppError(CodeFileChangedOnDisk, "The file was changed outside the editor.", false,
		map[string]any{"path": path, "currentContentHash": currentContentHash}, nil)
}

func RequestTooLarge(limit int64) *AppError {
	return newAppError(CodeRequestTooLarge, "The request exceeds the maximum allowed size.", false,
		map[string]any{"limitBytes": limit}, nil)
}

func UnsupportedLanguage(path string) *AppError {
	return newAppError(CodeUnsupportedLanguage, "This language is not supported for this operation.", false,
		map[string]any{"path": path}, nil)
}

func SourceParseFailed(path string, cause error) *AppError {
	return newAppError(CodeSourceParseFailed, "The source code could not be parsed.", false,
		map[string]any{"path": path}, cause)
}

// ---- Symbol / retrieval ----

func SymbolNotFound(path string, line, column int) *AppError {
	return newAppError(CodeSymbolNotFound, "No symbol was found at the requested position.", false,
		map[string]any{"path": path, "line": line, "column": column}, nil)
}

func SymbolAmbiguous(symbol string, count int) *AppError {
	return newAppError(CodeSymbolAmbiguous, "The symbol is ambiguous; refine the selection.", false,
		map[string]any{"symbol": symbol, "count": count}, nil)
}

func QueryRequired() *AppError {
	return newAppError(CodeQueryRequired, "Enter a query.", false, nil, nil)
}

func NoRelevantEvidence(query string) *AppError {
	return newAppError(CodeNoRelevantEvidence, "No relevant evidence was found for the query.", false,
		map[string]any{"query": query}, nil)
}

// ---- State / concurrency ----

func AppNotReady(reason string) *AppError {
	details := map[string]any{}
	if reason != "" {
		details["reason"] = reason
	}
	return newAppError(CodeAppNotReady, "The service is not ready yet.", true, details, nil)
}

func IndexingInProgress() *AppError {
	return newAppError(CodeIndexingInProgress, "Indexing is already in progress.", true, nil, nil)
}

func StoreVersionConflict() *AppError {
	return newAppError(CodeStoreVersionConflict, "The index changed during the operation; try again.", true, nil, nil)
}

// DocumentAlreadyOpen carries only sanitized metadata (the path), never the lease
// or content.
func DocumentAlreadyOpen(path string) *AppError {
	return newAppError(CodeDocumentAlreadyOpen, "The document is already open for editing.", false, map[string]any{"path": path}, nil)
}

// DocumentDirty is returned when closing an unsaved document without discard.
func DocumentDirty(documentID string) *AppError {
	return newAppError(CodeDocumentDirty, "The document has unsaved changes.", false, map[string]any{"documentId": documentID}, nil)
}

func DocumentNotFound(documentID string) *AppError {
	return newAppError(CodeDocumentNotFound, "Document not found.", false, map[string]any{"documentId": documentID}, nil)
}

func DocumentConflictChanged(documentID string) *AppError {
	return newAppError(CodeDocumentConflictChanged, "The document conflict changed; reload its state before resolving it.", true,
		map[string]any{"documentId": documentID}, nil)
}

func DocumentConflictNotFound(documentID string) *AppError {
	return newAppError(CodeDocumentConflictNotFound, "This document has no active conflict.", false,
		map[string]any{"documentId": documentID}, nil)
}

func DocumentResolutionInProgress(documentID string) *AppError {
	return newAppError(CodeDocumentResolutionInProgress, "Conflict resolution is already in progress.", true,
		map[string]any{"documentId": documentID}, nil)
}

// OverlayLimitExceeded is returned when an open/replace would exceed a configured
// overlay limit; a dirty buffer is never evicted to make room.
func OverlayLimitExceeded(detail string) *AppError {
	return newAppError(CodeOverlayLimitExceeded, "Open document limit reached.", true, map[string]any{"detail": detail}, nil)
}

func DocumentVersionConflict(path string) *AppError {
	return newAppError(CodeDocumentVersionConflict, "The document changed while it was being edited; reload it.", true,
		map[string]any{"path": path}, nil)
}

func DeepWikiGenerationInProgress() *AppError {
	return newAppError(CodeDeepWikiInProgress, "DeepWiki generation is already in progress.", true, nil, nil)
}

func JobQueueFull() *AppError {
	return newAppError(CodeJobQueueFull, "The job queue is full.", true, nil, nil)
}

func JobNotFound(id string) *AppError {
	return newAppError(CodeJobNotFound, "Job not found.", false, map[string]any{"jobId": id}, nil)
}

func JobRevisionConflict(id string) *AppError {
	return newAppError(CodeJobRevisionConflict, "The job revision changed; reload it before canceling.", true, map[string]any{"jobId": id}, nil)
}

func JobTypeUnsupported(jobType string) *AppError {
	return newAppError(CodeJobTypeUnsupported, "Unsupported job type.", false, map[string]any{"type": jobType}, nil)
}

func JobResultUnavailable(id string) *AppError {
	return newAppError(CodeJobResultUnavailable, "The job result is not available yet.", true, map[string]any{"jobId": id}, nil)
}

// ---- Infrastructure ----

func ProviderUnavailable(cause error) *AppError {
	return newAppError(CodeProviderUnavailable, "The AI provider is unavailable.", true, nil, cause)
}

func ProviderTimeout(cause error) *AppError {
	return newAppError(CodeProviderTimeout, "The AI provider timed out.", true, nil, cause)
}

// ProviderUnauthorized never carries the credential or the upstream body.
func ProviderUnauthorized() *AppError {
	return newAppError(CodeProviderUnauthorized, "The AI provider rejected authentication.", false, nil, nil)
}

// ModelOutputInvalid means the model's structured output failed grounding/schema
// validation after a controlled retry; no partial result is exposed or persisted.
// The cause is a sanitized validation summary, never raw model content.
func ModelOutputInvalid(cause error) *AppError {
	return newAppError(CodeModelOutputInvalid, "The model response failed grounding validation.", true, nil, cause)
}

func EmbeddingUnavailable(cause error) *AppError {
	return newAppError(CodeEmbeddingUnavailable, "Dense search (embeddings) is unavailable.", true, nil, cause)
}

func StoreCorrupted(cause error) *AppError {
	return newAppError(CodeStoreCorrupted, "The index is corrupted.", false, nil, cause)
}

func PersistenceFailed(cause error) *AppError {
	return newAppError(CodePersistenceFailed, "Failed to persist the index.", true, nil, cause)
}

// DatabaseOpenFailed marks a failure to open or configure the SQLite database
// (including a rejected mandatory pragma).
func DatabaseOpenFailed(cause error) *AppError {
	return newAppError(CodeDatabaseOpenFailed, "Failed to open the database.", false, nil, cause)
}

// DatabaseMigrationFailed marks a migration that failed to apply; the prior schema
// is left intact.
func DatabaseMigrationFailed(cause error) *AppError {
	return newAppError(CodeDatabaseMigrationFailed, "Failed to apply a database migration.", false, nil, cause)
}

// DatabaseMigrationChecksumMismatch marks an already-applied migration whose
// recorded checksum no longer matches its content — an incompatible on-disk schema.
func DatabaseMigrationChecksumMismatch(details map[string]any) *AppError {
	return newAppError(CodeDatabaseMigrationChecksumMismatch, "The database schema is incompatible with this version.", false, details, nil)
}

// DatabaseVersionTooNew marks a database whose schema version is newer than this
// build supports.
func DatabaseVersionTooNew(details map[string]any) *AppError {
	return newAppError(CodeDatabaseVersionTooNew, "The database was created by a newer version.", false, details, nil)
}

// ArtifactInputSnapshotStale marks a publish whose InputSnapshotID no longer
// matches the current structural snapshot (strict v1 policy).
func ArtifactInputSnapshotStale() *AppError {
	return newAppError(CodeArtifactInputSnapshotStale, "The artifact was generated from a snapshot that has changed.", true, nil, nil)
}

// ArtifactNotFound marks a missing artifact or head.
func ArtifactNotFound() *AppError {
	return newAppError(CodeArtifactNotFound, "Artifact not found.", false, nil, nil)
}

// InternalError is the catch-all for an untyped or unexpected failure; the cause
// is logged and never exposed.
func InternalError(cause error) *AppError {
	return newAppError(CodeInternalError, "Internal error.", false, nil, cause)
}

// RequestCanceled marks a client-cancelled or deadline-exceeded request.
func RequestCanceled(cause error) *AppError {
	return newAppError(CodeRequestCanceled, "Request canceled.", false, nil, cause)
}
