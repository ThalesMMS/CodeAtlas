package gopls

import (
	"context"
	"encoding/json"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// handlePublishDiagnostics ingests a publishDiagnostics notification, replacing
// the prior set for that URI. The message is untrusted text; quantities and bytes
// are bounded; an empty set clears the URI's diagnostics.
func (m *Manager) handlePublishDiagnostics(params json.RawMessage) {
	m.diagnostics.Ingest(params)
}

// invalidateUnknownDiagnostics drops a URI's version-unknown diagnostics after a
// didChange, since they can no longer be trusted to describe the new content.
func (m *Manager) invalidateUnknownDiagnostics(uri string) {
	m.diagnostics.InvalidateUnknown(uri)
}

// diagnosticsFor returns a copy of the stored set for a URI.
// Diagnostics returns diagnostic facts for the query's document. A version-known
// set whose version does not match the query is stale and yields nothing; a
// version-unknown set yields facts flagged VersionKnown=false (uncertain).
func (p *Provider) Diagnostics(_ context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	if _, err := p.manager.operational(); err != nil {
		return nil, err
	}
	uri := p.manager.documentURI(query.Path)
	set, ok := p.manager.diagnostics.Get(uri)
	if !ok {
		return nil, nil
	}
	if set.VersionKnown && query.UsesOpenDocument() && set.Version != int64(query.DocumentVersion) {
		return nil, nil // stale relative to the requested version
	}
	facts := make([]semantic.SemanticFact, 0, len(set.Items))
	converter := p.locationConverter(query, nil)
	for _, diagnostic := range set.Items {
		location, ok := converter.Convert(lspfacts.Location{URI: uri, Range: diagnostic.Range})
		// A diagnostic whose range failed to convert against the document content
		// is skipped, never crashing the provider.
		if !ok || location.Range == (zeroRange) {
			continue
		}
		fact := p.fact(query, semantic.KindDiagnostic, "textDocument/publishDiagnostics", location, lspadapter.DiagnosticDetail(diagnostic))
		normalized := lspadapter.NormalizeDiagnostic(diagnostic)
		fact.Diagnostic = &normalized
		fact.Confidence = semantic.ConfidenceDiagnostic // certainty it was reported, not that it is true
		fact.VersionKnown = set.VersionKnown
		if set.VersionKnown {
			fact.DocumentVersion = semantic.DocumentVersion(set.Version)
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

var zeroRange = domain.Range{}
