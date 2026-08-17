package apperror

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestHTTPStatusForEveryCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code   Code
		status int
	}{
		{CodeInvalidArgument, http.StatusBadRequest},
		{CodePathOutsideWorkspace, http.StatusBadRequest},
		{CodeFileNotFound, http.StatusNotFound},
		{CodeFileChangedOnDisk, http.StatusConflict},
		{CodeRequestTooLarge, http.StatusRequestEntityTooLarge},
		{CodeUnsupportedLanguage, http.StatusUnprocessableEntity},
		{CodeSourceParseFailed, http.StatusUnprocessableEntity},
		{CodeSymbolNotFound, http.StatusNotFound},
		{CodeSymbolAmbiguous, http.StatusUnprocessableEntity},
		{CodeQueryRequired, http.StatusBadRequest},
		{CodeNoRelevantEvidence, http.StatusUnprocessableEntity},
		{CodeAppNotReady, http.StatusServiceUnavailable},
		{CodeIndexingInProgress, http.StatusConflict},
		{CodeStoreVersionConflict, http.StatusConflict},
		{CodeDocumentVersionConflict, http.StatusConflict},
		{CodeDocumentConflictChanged, http.StatusConflict},
		{CodeDocumentConflictNotFound, http.StatusNotFound},
		{CodeDocumentResolutionInProgress, http.StatusConflict},
		{CodeDeepWikiInProgress, http.StatusConflict},
		{CodeJobQueueFull, http.StatusTooManyRequests},
		{CodeJobNotFound, http.StatusNotFound},
		{CodeJobRevisionConflict, http.StatusConflict},
		{CodeJobTypeUnsupported, http.StatusBadRequest},
		{CodeJobResultUnavailable, http.StatusConflict},
		{CodeProviderUnavailable, http.StatusServiceUnavailable},
		{CodeProviderTimeout, http.StatusGatewayTimeout},
		{CodeProviderUnauthorized, http.StatusServiceUnavailable},
		{CodeEmbeddingUnavailable, http.StatusServiceUnavailable},
		{CodeStoreCorrupted, http.StatusServiceUnavailable},
		{CodePersistenceFailed, http.StatusServiceUnavailable},
		{CodeInternalError, http.StatusInternalServerError},
		{CodeRequestCanceled, 499},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			if got := HTTPStatus(&AppError{Code: tc.code}); got != tc.status {
				t.Fatalf("HTTPStatus(%s) = %d, want %d", tc.code, got, tc.status)
			}
		})
	}
}

func TestEveryCodeHasAStatus(t *testing.T) {
	t.Parallel()
	all := []Code{
		CodeInvalidArgument, CodePathOutsideWorkspace, CodeFileNotFound, CodeFileChangedOnDisk,
		CodeRequestTooLarge, CodeUnsupportedLanguage, CodeSourceParseFailed, CodeSymbolNotFound,
		CodeSymbolAmbiguous, CodeQueryRequired, CodeNoRelevantEvidence, CodeAppNotReady,
		CodeIndexingInProgress, CodeStoreVersionConflict, CodeDocumentVersionConflict, CodeDocumentAlreadyOpen,
		CodeDocumentDirty, CodeDocumentNotFound, CodeDocumentConflictChanged, CodeDocumentConflictNotFound,
		CodeDocumentResolutionInProgress, CodeOverlayLimitExceeded, CodeDeepWikiInProgress,
		CodeJobQueueFull, CodeJobNotFound, CodeJobRevisionConflict, CodeJobTypeUnsupported, CodeJobResultUnavailable,
		CodeProviderUnavailable, CodeProviderTimeout, CodeProviderUnauthorized, CodeModelOutputInvalid,
		CodeEmbeddingUnavailable, CodeStoreCorrupted, CodePersistenceFailed, CodeInternalError, CodeRequestCanceled,
		CodeDatabaseOpenFailed, CodeDatabaseMigrationFailed, CodeDatabaseMigrationChecksumMismatch, CodeDatabaseVersionTooNew,
		CodeArtifactInputSnapshotStale, CodeArtifactNotFound,
	}
	for _, code := range all {
		if _, ok := statusByCode[code]; !ok {
			t.Errorf("code %s has no HTTP status mapping", code)
		}
	}
	if len(all) != len(statusByCode) {
		t.Fatalf("catalog has %d codes but statusByCode has %d; keep them in sync", len(all), len(statusByCode))
	}
}

func TestHTTPStatusDefaultsTo500(t *testing.T) {
	t.Parallel()
	if got := HTTPStatus(errors.New("plain error")); got != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus(plain) = %d, want 500", got)
	}
	if got := HTTPStatus(&AppError{Code: "MADE_UP_CODE"}); got != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus(unknown code) = %d, want 500", got)
	}
}

func TestErrorReturnsPublicMessageNotCause(t *testing.T) {
	t.Parallel()
	secret := errors.New("api_key=sk-INTERNAL-SECRET at https://host/v1")
	err := ProviderUnavailable(secret)
	if got := err.Error(); got != "The AI provider is unavailable." {
		t.Fatalf("Error() = %q, want the public message", got)
	}
	// The cause must be reachable for logging but never part of the public string.
	if err.Error() == secret.Error() {
		t.Fatal("Error() exposed the cause")
	}
	if !errors.Is(err, secret) {
		t.Fatal("cause is not reachable via errors.Is for logging")
	}
}

func TestErrorsAsAndWrapping(t *testing.T) {
	t.Parallel()
	base := FileNotFound("a.go", nil)
	wrapped := fmt.Errorf("reading view: %w", base)

	appErr, ok := As(wrapped)
	if !ok || appErr.Code != CodeFileNotFound {
		t.Fatalf("As(wrapped) = %v/%v, want FILE_NOT_FOUND", appErr, ok)
	}
	if HTTPStatus(wrapped) != http.StatusNotFound {
		t.Fatalf("status across wrap = %d, want 404", HTTPStatus(wrapped))
	}
	if !errors.Is(wrapped, &AppError{Code: CodeFileNotFound}) {
		t.Fatal("errors.Is by code failed across a wrap")
	}
	if errors.Is(wrapped, &AppError{Code: CodeInternalError}) {
		t.Fatal("errors.Is matched the wrong code")
	}
}

func TestConstructorsSetExpectedFields(t *testing.T) {
	t.Parallel()
	changed := FileChangedOnDisk("web/checkout.ts", "sha256:abc")
	if changed.Code != CodeFileChangedOnDisk || changed.Retryable {
		t.Fatalf("FileChangedOnDisk = %+v", changed)
	}
	if changed.Details["path"] != "web/checkout.ts" || changed.Details["currentContentHash"] != "sha256:abc" {
		t.Fatalf("FileChangedOnDisk details = %v", changed.Details)
	}

	retryable := []*AppError{
		IndexingInProgress(), StoreVersionConflict(), DeepWikiGenerationInProgress(),
		JobQueueFull(), JobRevisionConflict("job:1"), JobResultUnavailable("job:1"),
		ProviderUnavailable(nil), ProviderTimeout(nil), EmbeddingUnavailable(nil), AppNotReady("x"),
	}
	for _, err := range retryable {
		if !err.Retryable {
			t.Errorf("%s should be retryable", err.Code)
		}
	}
	notRetryable := []*AppError{
		InvalidArgument("x", nil), FileNotFound("a", nil), QueryRequired(), SymbolNotFound("a", 1, 1),
		ProviderUnauthorized(), StoreCorrupted(nil), InternalError(nil),
	}
	for _, err := range notRetryable {
		if err.Retryable {
			t.Errorf("%s should not be retryable", err.Code)
		}
	}
}

func TestProviderUnauthorizedCarriesNoDetails(t *testing.T) {
	t.Parallel()
	err := ProviderUnauthorized()
	if len(err.Details) != 0 || err.Cause != nil {
		t.Fatalf("ProviderUnauthorized must not carry details/cause: %+v", err)
	}
}
