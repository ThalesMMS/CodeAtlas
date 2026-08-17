package semantic

import "fmt"

// ConfidencePolicyVersion versions the confidence assignment below; any change to
// the numbers bumps it.
const ConfidencePolicyVersion = "confidence.v1"

// Confidence levels for v1. Confidence is always finite in [0,1]; provenance is
// never inferred from it.
const (
	// ConfidenceASTDeclaration: a declaration/range observed directly in the AST.
	ConfidenceASTDeclaration = 1.0
	// ConfidenceASTNameCall: a call inferred only by name in the AST.
	ConfidenceASTNameCall = 0.60
	// ConfidenceStructuralUnresolved: an unresolved structural import/reference
	// (documented 0.55–0.70 band; 0.65 is the v1 point value).
	ConfidenceStructuralUnresolved = 0.65
	// ConfidenceLanguageServerResolved: a definition/reference/call resolved by a
	// language server.
	ConfidenceLanguageServerResolved = 0.95
	// ConfidenceDiagnostic: certainty that the tool reported it, not that the
	// conclusion is absolute truth.
	ConfidenceDiagnostic = 1.0
	// ConfidenceRuntimeTrace: certainty for one observed execution.
	ConfidenceRuntimeTrace = 1.0
)

// Provenance source identifiers used by the confidence policy.
const (
	SourceTreeSitter    = "tree-sitter"
	SourceGopls         = "gopls"
	SourceTypeScriptLSP = "typescript-lsp"
	SourceSwiftLSP      = "sourcekit-lsp"
	SourcePythonLSP     = "pyright-langserver"
	SourceRustLSP       = "rust-analyzer"
	SourceRuntime       = "runtime"
)

// ConfidenceFor returns the v1 confidence for a (source, kind, resolved) triple.
// resolved distinguishes a resolved language-server fact from an AST name-only
// inference. It is the single place confidence numbers live.
func ConfidenceFor(source, kind string, resolved bool) float64 {
	switch source {
	case SourceGopls, SourceTypeScriptLSP, SourceSwiftLSP, SourcePythonLSP, SourceRustLSP:
		if kind == KindDiagnostic {
			return ConfidenceDiagnostic
		}
		return ConfidenceLanguageServerResolved
	case SourceRuntime:
		return ConfidenceRuntimeTrace
	case SourceTreeSitter:
		switch kind {
		case KindDefinition, KindHoverType:
			return ConfidenceASTDeclaration
		case KindSyntacticCall, KindCallOutgoing, KindCallIncoming:
			if resolved {
				return ConfidenceASTDeclaration
			}
			return ConfidenceASTNameCall
		case KindReference:
			return ConfidenceStructuralUnresolved
		}
	}
	if resolved {
		return ConfidenceLanguageServerResolved
	}
	return ConfidenceStructuralUnresolved
}

// ValidateFact checks a fact's required fields, finite confidence and provenance.
// Provenance.Source must be present; confidence must be finite in [0,1].
func ValidateFact(fact SemanticFact) error {
	if fact.Kind == "" {
		return fmt.Errorf("semantic: fact has no kind")
	}
	if fact.Provenance.Source == "" {
		return fmt.Errorf("semantic: fact %q has no provenance source", fact.Kind)
	}
	if fact.Provenance.ObservedAt.IsZero() {
		return fmt.Errorf("semantic: fact %q has no observedAt", fact.Kind)
	}
	if !finiteInUnit(fact.Confidence) {
		return fmt.Errorf("semantic: fact %q confidence %v not finite in [0,1]", fact.Kind, fact.Confidence)
	}
	return nil
}

func finiteInUnit(value float64) bool {
	return value >= 0 && value <= 1
}
