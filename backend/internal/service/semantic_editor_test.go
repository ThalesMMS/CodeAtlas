package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/parsesession"
)

func TestNormalizeParseSessionErrorUsesTypedClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code apperror.Code
	}{
		{name: "missing", err: fmt.Errorf("wrapped: %w", parsesession.ErrSessionNotFound), code: apperror.CodeDocumentNotFound},
		{name: "version", err: fmt.Errorf("wrapped: %w", parsesession.ErrVersionUnavailable), code: apperror.CodeDocumentVersionConflict},
		{name: "text does not classify", err: errors.New("database version lookup failed"), code: apperror.CodeInternalError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appErr, ok := apperror.As(normalizeParseSessionError("doc-1", test.err))
			if !ok || appErr.Code != test.code {
				t.Fatalf("normalized error = %#v, want %s", appErr, test.code)
			}
		})
	}
}

func TestDiagnosticTruncationKeepsParserAndLSPAttributionSeparate(t *testing.T) {
	t.Parallel()
	parser, ok := diagnosticTruncationOmission("parser_diagnostics", 7, 5, 5)
	if !ok || parser.Ref != "parser_diagnostics" || parser.Count != 2 {
		t.Fatalf("parser omission = %#v, %v", parser, ok)
	}
	lsp, ok := diagnosticTruncationOmission("lsp_diagnostics", 4, 0, 5)
	if !ok || lsp.Ref != "lsp_diagnostics" || lsp.Count != 4 {
		t.Fatalf("LSP omission = %#v, %v", lsp, ok)
	}
	if _, ok := diagnosticTruncationOmission("lsp_diagnostics", 2, 2, 5); ok {
		t.Fatal("non-truncated diagnostics produced an omission")
	}
}
