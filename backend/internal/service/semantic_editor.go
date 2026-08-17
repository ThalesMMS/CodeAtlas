package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/overlay"
	"github.com/ThalesMMS/CodeAtlas/internal/parsesession"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

const (
	defaultMaxDiagnostics = 1_000
	maxDiagnosticMessage  = 1_000
)

type SemanticEditorService struct {
	store          repository.Store
	overlays       *overlay.Store
	parseSessions  *parsesession.Manager
	maxDiagnostics int
	provider       semantic.SemanticProvider
}

func (s *SemanticEditorService) SetSemanticProvider(provider semantic.SemanticProvider) {
	s.provider = provider
}

func NewSemanticEditorService(store repository.Store, overlays *overlay.Store, parseSessions *parsesession.Manager) *SemanticEditorService {
	return &SemanticEditorService{
		store:          store,
		overlays:       overlays,
		parseSessions:  parseSessions,
		maxDiagnostics: defaultMaxDiagnostics,
	}
}

func (s *SemanticEditorService) Diagnostics(ctx context.Context, documentID string, version int64) (domain.DiagnosticsResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.DiagnosticsResult{}, err
	}
	snapshot, parseSnapshot, err := s.openSemanticDocument(documentID, version, "")
	if err != nil {
		return domain.DiagnosticsResult{}, err
	}
	diagnostics := make([]domain.Diagnostic, 0)
	parserTotal := 0
	if parseSnapshot.HasErrors {
		err = s.parseSessions.WithTree(documentID, &version, func(root treesitter.Node, source []byte) error {
			diagnostics, parserTotal = parserDiagnostics(string(snapshot.DocumentID), snapshot.Path, int64(snapshot.Version), root, source, s.maxDiagnostics)
			return nil
		})
		if err != nil {
			return domain.DiagnosticsResult{}, normalizeParseSessionError(documentID, err)
		}
	}
	parserAccepted := len(diagnostics)
	lspTotal := 0
	lspAccepted := 0
	omissions := make([]domain.SemanticOmission, 0)
	lspState := s.providerState(snapshot.Path)
	if lspState.State == semantic.ProviderStateAvailable {
		facts, providerErr := s.provider.Diagnostics(ctx, semantic.SemanticQuery{
			SnapshotID:      semanticSnapshotID(s.store, snapshot),
			DocumentID:      semantic.DocumentID(snapshot.DocumentID),
			DocumentVersion: semantic.DocumentVersion(snapshot.Version),
			Path:            snapshot.Path, Position: domain.Position{Line: 1, Column: 1}, Content: snapshot.Content,
		})
		if providerErr != nil {
			lspState.State = semanticProviderErrorState(providerErr)
			omissions = append(omissions, semanticProviderOmission("lsp_diagnostics", lspState.State))
		} else {
			finalState := s.providerState(snapshot.Path)
			if providerSessionChanged(lspState, finalState) {
				lspState = finalState
				lspState.State = "restarted"
				omissions = append(omissions, semanticProviderOmission("lsp_diagnostics", lspState.State))
			} else {
				accepted, skipped, validTotal := semanticDiagnostics(snapshot, lspState.SessionID, facts, s.maxDiagnostics-len(diagnostics))
				diagnostics = append(diagnostics, accepted...)
				lspAccepted = len(accepted)
				lspTotal = validTotal
				if skipped > 0 {
					omissions = append(omissions, domain.SemanticOmission{
						Reason: "version_mismatch", Ref: "lsp_diagnostics",
						Detail: "diagnostics without exact document version, content hash and provider session were omitted", Count: skipped,
					})
				}
			}
		}
	} else {
		omissions = append(omissions, semanticProviderOmission("lsp_diagnostics", lspState.State))
	}
	sortDiagnostics(diagnostics)
	truncated := parserTotal > parserAccepted || lspTotal > lspAccepted
	if omission, ok := diagnosticTruncationOmission("parser_diagnostics", parserTotal, parserAccepted, s.maxDiagnostics); ok {
		omissions = append(omissions, omission)
	}
	if omission, ok := diagnosticTruncationOmission("lsp_diagnostics", lspTotal, lspAccepted, s.maxDiagnostics); ok {
		omissions = append(omissions, omission)
	}
	return domain.DiagnosticsResult{
		DocumentID:      string(snapshot.DocumentID),
		DocumentVersion: int64(snapshot.Version),
		ContentHash:     snapshot.ContentHash,
		SnapshotID:      semanticSnapshotID(s.store, snapshot),
		ViewHash:        semanticViewHash(snapshot, parseSnapshot),
		ProviderSession: lspState.SessionID,
		Diagnostics:     diagnostics,
		SemanticCoverage: map[string]any{
			"parser":          "available",
			"lsp":             lspState.State,
			"provider":        s.providerID(snapshot.Path),
			"providerSession": lspState.SessionID,
			"llm":             false,
		},
		Truncated: truncated,
		Omissions: omissions,
	}, nil
}

func diagnosticTruncationOmission(ref string, total, accepted, limit int) (domain.SemanticOmission, bool) {
	if total <= accepted {
		return domain.SemanticOmission{}, false
	}
	return domain.SemanticOmission{
		Reason: "truncated", Ref: ref, Detail: fmt.Sprintf("limited to %d diagnostics", limit), Count: total - accepted,
	}, true
}

func (s *SemanticEditorService) SemanticTokens(ctx context.Context, documentID string, version int64) (domain.SemanticTokensResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SemanticTokensResult{}, err
	}
	snapshot, parseSnapshot, err := s.openSemanticDocument(documentID, version, "")
	if err != nil {
		return domain.SemanticTokensResult{}, err
	}
	result := domain.SemanticTokensResult{
		LegendVersion: semantic.SemanticTokenLegendVersion,
		DocumentID:    string(snapshot.DocumentID), DocumentVersion: int64(snapshot.Version),
		ContentHash: snapshot.ContentHash, SnapshotID: semanticSnapshotID(s.store, snapshot),
		ViewHash: semanticViewHash(snapshot, parseSnapshot), Tokens: []domain.SemanticToken{},
		SemanticCoverage: map[string]any{"coverage": "none", "provider": s.providerID(snapshot.Path), "providerState": semantic.ProviderStateDisabled, "llm": false},
	}
	state := s.providerState(snapshot.Path)
	result.ProviderSession = state.SessionID
	result.SemanticCoverage["providerState"] = state.State
	if state.State != semantic.ProviderStateAvailable {
		result.Omissions = []domain.SemanticOmission{semanticProviderOmission("semantic_tokens", state.State)}
		return result, nil
	}
	capabilities, err := s.capabilitiesForPath(ctx, snapshot.Path)
	if err != nil {
		providerState := semanticProviderErrorState(err)
		result.SemanticCoverage["providerState"] = providerState
		result.Omissions = []domain.SemanticOmission{semanticProviderOmission("semantic_tokens", providerState)}
		return result, nil
	}
	if !capabilities.SemanticTokensFull {
		result.SemanticCoverage["providerState"] = "unsupported"
		result.Omissions = []domain.SemanticOmission{{Reason: "provider_unsupported", Ref: "semantic_tokens", Detail: "semantic token capability is not supported for this language"}}
		return result, nil
	}
	provider, ok := s.provider.(semantic.SemanticTokenProvider)
	if !ok {
		result.SemanticCoverage["providerState"] = "unsupported"
		result.Omissions = []domain.SemanticOmission{{Reason: "provider_unsupported", Ref: "semantic_tokens", Detail: "semantic token provider is not configured"}}
		return result, nil
	}
	set, err := provider.SemanticTokens(ctx, semantic.SemanticQuery{
		SnapshotID: result.SnapshotID, DocumentID: semantic.DocumentID(snapshot.DocumentID),
		DocumentVersion: semantic.DocumentVersion(snapshot.Version), Path: snapshot.Path,
		Position: domain.Position{Line: 1, Column: 1}, Content: snapshot.Content,
	})
	if err != nil {
		providerState := semanticProviderErrorState(err)
		result.SemanticCoverage["providerState"] = providerState
		result.Omissions = []domain.SemanticOmission{semanticProviderOmission("semantic_tokens", providerState)}
		return result, nil
	}
	finalState := s.providerState(snapshot.Path)
	if providerSessionChanged(state, finalState) {
		result.ProviderSession = finalState.SessionID
		result.SemanticCoverage["providerState"] = "restarted"
		result.Omissions = []domain.SemanticOmission{semanticProviderOmission("semantic_tokens", "restarted")}
		return result, nil
	}
	if set.DocumentID != semantic.DocumentID(snapshot.DocumentID) || set.DocumentVersion != semantic.DocumentVersion(snapshot.Version) ||
		set.ContentHash != snapshot.ContentHash || set.ProviderSession == "" || set.ProviderSession != state.SessionID {
		result.SemanticCoverage["providerState"] = "stale"
		result.Omissions = []domain.SemanticOmission{{Reason: "version_mismatch", Ref: "semantic_tokens", Detail: "semantic tokens did not match the exact document version, content hash and provider session"}}
		return result, nil
	}
	result.ProviderSession = set.ProviderSession
	result.Truncated = set.Truncated
	var previous *domain.Range
	for _, token := range set.Tokens {
		rng, ok := internalRangeToUTF16(snapshot.Content, token.Range)
		if !ok || rng.Start.Line != rng.End.Line || comparePositions(rng.Start, rng.End) >= 0 ||
			(previous != nil && comparePositions(rng.Start, previous.End) < 0) || !canonicalSemanticToken(token) {
			result.SemanticCoverage["providerState"] = "invalid"
			result.Tokens = []domain.SemanticToken{}
			result.Omissions = []domain.SemanticOmission{{Reason: "provider_payload_invalid", Ref: "semantic_tokens", Detail: "semantic token range could not be validated against the acknowledged content"}}
			return result, nil
		}
		result.Tokens = append(result.Tokens, domain.SemanticToken{Range: rng, TokenType: token.TokenType, Modifiers: append([]string(nil), token.Modifiers...)})
		copied := rng
		previous = &copied
	}
	if set.Truncated {
		result.Omissions = append(result.Omissions, domain.SemanticOmission{
			Reason: "truncated", Ref: "semantic_tokens", Detail: fmt.Sprintf("limited to %d semantic tokens", len(result.Tokens)), Count: set.OmittedCount,
		})
	}
	result.SemanticCoverage["coverage"] = "lsp"
	result.SemanticCoverage["providerState"] = semantic.ProviderStateAvailable
	return result, nil
}

func providerSessionChanged(before, after semantic.ProviderState) bool {
	return before.SessionID != "" && after.SessionID != "" && before.SessionID != after.SessionID
}

func (s *SemanticEditorService) openSemanticDocument(documentID string, version int64, path string) (overlay.OverlaySnapshot, parsesession.Snapshot, error) {
	if s == nil || s.overlays == nil || s.parseSessions == nil {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, apperror.InternalError(fmt.Errorf("semantic editor service not configured"))
	}
	if documentID == "" {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, apperror.InvalidArgumentMessage("documentId", "documentId is required.", nil)
	}
	if version <= 0 {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, apperror.InvalidArgumentMessage("documentVersion", "documentVersion is required.", nil)
	}
	overlayVersion := overlay.DocumentVersion(version)
	snapshot, err := s.overlays.Get(overlay.DocumentID(documentID), &overlayVersion)
	if err != nil {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, err
	}
	if path != "" && path != snapshot.Path {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, apperror.InvalidArgumentMessage("path", "path does not match documentId.", nil)
	}
	parseVersion := int64(snapshot.Version)
	parseSnapshot, err := s.parseSessions.Get(documentID, &parseVersion)
	if err != nil {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, normalizeParseSessionError(documentID, err)
	}
	if parseSnapshot.Version != int64(snapshot.Version) {
		return overlay.OverlaySnapshot{}, parsesession.Snapshot{}, apperror.DocumentVersionConflict(snapshot.Path)
	}
	return snapshot, parseSnapshot, nil
}

func (s *SemanticEditorService) providerState(path string) semantic.ProviderState {
	if s == nil || s.provider == nil {
		return semantic.ProviderState{State: semantic.ProviderStateDisabled, Reason: "provider_not_configured"}
	}
	if routed, ok := s.provider.(interface {
		ProviderStateForPath(string) semantic.ProviderState
	}); ok {
		return routed.ProviderStateForPath(path)
	}
	if reporter, ok := s.provider.(semantic.ProviderStateReporter); ok {
		return reporter.ProviderState()
	}
	return semantic.ProviderState{State: semantic.ProviderStateDisabled, Reason: "provider_state_not_reported"}
}

func (s *SemanticEditorService) providerID(path string) string {
	if s == nil || s.provider == nil {
		return "none"
	}
	if routed, ok := s.provider.(interface {
		ProviderIDForPath(string) (semantic.SemanticProviderID, bool)
	}); ok {
		if id, found := routed.ProviderIDForPath(path); found {
			return string(id)
		}
	}
	return string(s.provider.ID())
}

func (s *SemanticEditorService) capabilitiesForPath(ctx context.Context, path string) (semantic.SemanticCapabilities, error) {
	if routed, ok := s.provider.(interface {
		CapabilitiesForPath(context.Context, string) (semantic.SemanticCapabilities, error)
	}); ok {
		return routed.CapabilitiesForPath(ctx, path)
	}
	return s.provider.Capabilities(ctx)
}

func semanticProviderErrorState(err error) string {
	switch {
	case errors.Is(err, semantic.ErrCapabilityUnsupported):
		return "unsupported"
	case errors.Is(err, semantic.ErrProviderRestarted):
		return "restarted"
	case errors.Is(err, semantic.ErrProviderPayloadInvalid):
		return "invalid"
	case errors.Is(err, semantic.ErrProviderStale):
		return "stale"
	default:
		return semantic.ProviderStateUnavailable
	}
}

func semanticProviderOmission(ref, state string) domain.SemanticOmission {
	reason := "provider_" + state
	detail := "semantic provider is " + state
	if state == "restarted" {
		detail = "semantic provider restarted while the request was in flight"
	} else if state == "invalid" {
		detail = "semantic provider returned an invalid bounded payload"
	}
	return domain.SemanticOmission{Reason: reason, Ref: ref, Detail: detail}
}

func semanticDiagnostics(snapshot overlay.OverlaySnapshot, providerSession string, facts []semantic.SemanticFact, limit int) ([]domain.Diagnostic, int, int) {
	if limit < 0 {
		limit = 0
	}
	diagnostics := make([]domain.Diagnostic, 0, min(limit, len(facts)))
	skipped := 0
	validTotal := 0
	for _, fact := range facts {
		if fact.Kind != semantic.KindDiagnostic || fact.Diagnostic == nil || !fact.VersionKnown ||
			fact.DocumentID != semantic.DocumentID(snapshot.DocumentID) || fact.DocumentVersion != semantic.DocumentVersion(snapshot.Version) ||
			fact.ContentHash != snapshot.ContentHash || providerSession == "" || fact.ProviderSession != providerSession || fact.Location.Path != snapshot.Path {
			skipped++
			continue
		}
		rng, ok := semanticLocationRangeUTF16(snapshot.Content, fact.Location)
		if !ok {
			skipped++
			continue
		}
		if len(diagnostics) >= limit {
			validTotal++
			continue
		}
		validTotal++
		payload := fact.Diagnostic
		diagnostic := domain.Diagnostic{
			Path: snapshot.Path, Range: rng, Severity: domain.DiagnosticSeverity(payload.Severity),
			Code: payload.Code, Source: payload.Source, Message: boundDiagnosticMessage(payload.Message),
			Tags: []string{}, Related: []domain.DiagnosticRelatedLocation{}, VersionKnown: true,
			Provenance: domain.SemanticProvenance{
				ProviderID: string(fact.Provenance.ProviderID), ToolVersion: fact.Provenance.ToolVersion, Method: fact.Provenance.Method,
			},
		}
		if diagnostic.Source == "" {
			diagnostic.Source = fact.Provenance.Source
		}
		diagnostic.DiagnosticID = diagnosticID(string(snapshot.DocumentID), snapshot.Path, int64(snapshot.Version), diagnostic)
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, skipped, validTotal
}

func semanticLocationRangeUTF16(source []byte, location semantic.SourceLocation) (domain.Range, bool) {
	if location.Encoding == semantic.EncodingUTF16 {
		starts := lspconv.LineStarts(source)
		startLine, startColumn, startErr := lspconv.LSPToInternal(source, starts, location.Range.Start.Line-1, location.Range.Start.Column-1, lspconv.EncodingUTF16)
		endLine, endColumn, endErr := lspconv.LSPToInternal(source, starts, location.Range.End.Line-1, location.Range.End.Column-1, lspconv.EncodingUTF16)
		if startErr != nil || endErr != nil {
			return domain.Range{}, false
		}
		return internalRangeToUTF16(source, domain.Range{
			Start: domain.Position{Line: startLine, Column: startColumn},
			End:   domain.Position{Line: endLine, Column: endColumn},
		})
	}
	if location.Encoding != semantic.EncodingUTF8 {
		return domain.Range{}, false
	}
	return internalRangeToUTF16(source, location.Range)
}

func internalRangeToUTF16(source []byte, rng domain.Range) (domain.Range, bool) {
	starts := lspconv.LineStarts(source)
	startLine, startColumn, err := lspconv.InternalToLSP(source, starts, rng.Start.Line, rng.Start.Column, lspconv.EncodingUTF16)
	if err != nil {
		return domain.Range{}, false
	}
	endLine, endColumn, err := lspconv.InternalToLSP(source, starts, rng.End.Line, rng.End.Column, lspconv.EncodingUTF16)
	if err != nil {
		return domain.Range{}, false
	}
	startRoundLine, startRoundColumn, startRoundErr := lspconv.LSPToInternal(source, starts, startLine, startColumn, lspconv.EncodingUTF16)
	endRoundLine, endRoundColumn, endRoundErr := lspconv.LSPToInternal(source, starts, endLine, endColumn, lspconv.EncodingUTF16)
	if startRoundErr != nil || endRoundErr != nil || startRoundLine != rng.Start.Line || startRoundColumn != rng.Start.Column ||
		endRoundLine != rng.End.Line || endRoundColumn != rng.End.Column || comparePositions(rng.Start, rng.End) > 0 {
		return domain.Range{}, false
	}
	return domain.Range{
		Start: domain.Position{Line: startLine + 1, Column: startColumn + 1, Encoding: "utf-16"},
		End:   domain.Position{Line: endLine + 1, Column: endColumn + 1, Encoding: "utf-16"},
	}, true
}

func canonicalSemanticToken(token semantic.SemanticToken) bool {
	if !containsString(semantic.CanonicalSemanticTokenTypes, token.TokenType) {
		return false
	}
	seen := make(map[string]struct{}, len(token.Modifiers))
	for _, modifier := range token.Modifiers {
		if !containsString(semantic.CanonicalSemanticTokenModifiers, modifier) {
			return false
		}
		if _, duplicate := seen[modifier]; duplicate {
			return false
		}
		seen[modifier] = struct{}{}
	}
	return true
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func comparePositions(left, right domain.Position) int {
	if left.Line < right.Line || (left.Line == right.Line && left.Column < right.Column) {
		return -1
	}
	if left.Line == right.Line && left.Column == right.Column {
		return 0
	}
	return 1
}

func parserDiagnostics(documentID, path string, version int64, root treesitter.Node, source []byte, limit int) ([]domain.Diagnostic, int) {
	if limit <= 0 {
		limit = defaultMaxDiagnostics
	}
	diagnostics := make([]domain.Diagnostic, 0)
	total := 0
	var walk func(treesitter.Node)
	walk = func(node treesitter.Node) {
		if node.IsNull() {
			return
		}
		if node.Type() == "ERROR" || node.IsMissing() {
			total++
			if len(diagnostics) < limit {
				diagnostics = append(diagnostics, parserDiagnostic(documentID, path, version, node, source))
			}
		}
		if !node.HasError() && !node.IsMissing() {
			return
		}
		for i := uint32(0); i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	return diagnostics, total
}

func parserDiagnostic(documentID, path string, version int64, node treesitter.Node, source []byte) domain.Diagnostic {
	rng := treeSitterRangeUTF16(node, source)
	code := "ERROR"
	message := "Tree-sitter parse error"
	if node.IsMissing() {
		code = "MISSING"
		message = "Tree-sitter missing syntax node"
	}
	diagnostic := domain.Diagnostic{
		Path:         path,
		Range:        rng,
		Severity:     domain.DiagnosticSeverityError,
		Code:         code,
		Source:       "tree-sitter",
		Message:      boundDiagnosticMessage(message),
		Tags:         []string{},
		Related:      []domain.DiagnosticRelatedLocation{},
		VersionKnown: true,
		Provenance: domain.SemanticProvenance{
			ProviderID: "tree-sitter",
			Method:     "parse",
		},
	}
	diagnostic.DiagnosticID = diagnosticID(documentID, path, version, diagnostic)
	return diagnostic
}

func treeSitterRangeUTF16(node treesitter.Node, source []byte) domain.Range {
	start := treeSitterPointUTF16(source, node.StartPoint())
	end := treeSitterPointUTF16(source, node.EndPoint())
	if end.Line < start.Line || (end.Line == start.Line && end.Column <= start.Column) {
		end = start
		end.Column++
	}
	return domain.Range{Start: start, End: end}
}

func treeSitterPointUTF16(source []byte, point treesitter.Point) domain.Position {
	lineStart, lineEnd := sourceLineBounds(source, int(point.Row))
	columnBytes := int(point.Column)
	lineLength := lineEnd - lineStart
	if columnBytes < 0 {
		columnBytes = 0
	}
	if columnBytes > lineLength {
		columnBytes = lineLength
	}
	prefix := string(source[lineStart : lineStart+columnBytes])
	return domain.Position{Line: int(point.Row) + 1, Column: utf16Column(prefix), Encoding: "utf-16"}
}

func sourceLineBounds(source []byte, row int) (int, int) {
	start := 0
	current := 0
	for index := 0; index < len(source); index++ {
		if source[index] != '\n' {
			continue
		}
		end := index
		if end > start && source[end-1] == '\r' {
			end--
		}
		if current == row {
			return start, end
		}
		current++
		start = index + 1
	}
	end := len(source)
	if end > start && source[end-1] == '\r' {
		end--
	}
	return start, end
}

func utf16Column(prefix string) int {
	units := 0
	for _, value := range prefix {
		width := utf16.RuneLen(value)
		if width < 1 {
			width = 1
		}
		units += width
	}
	return units + 1
}

func diagnosticID(documentID, path string, version int64, diagnostic domain.Diagnostic) string {
	raw := strings.Join([]string{
		documentID,
		path,
		fmt.Sprintf("%d", version),
		string(diagnostic.Severity),
		diagnostic.Source,
		diagnostic.Code,
		fmt.Sprintf("%d:%d:%d:%d", diagnostic.Range.Start.Line, diagnostic.Range.Start.Column, diagnostic.Range.End.Line, diagnostic.Range.End.Column),
		diagnostic.Message,
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "diag:" + hex.EncodeToString(sum[:12])
}

func sortDiagnostics(diagnostics []domain.Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		left, right := diagnostics[i], diagnostics[j]
		if severityRank(left.Severity) != severityRank(right.Severity) {
			return severityRank(left.Severity) < severityRank(right.Severity)
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Range.Start.Line != right.Range.Start.Line {
			return left.Range.Start.Line < right.Range.Start.Line
		}
		if left.Range.Start.Column != right.Range.Start.Column {
			return left.Range.Start.Column < right.Range.Start.Column
		}
		return left.DiagnosticID < right.DiagnosticID
	})
}

func severityRank(severity domain.DiagnosticSeverity) int {
	switch severity {
	case domain.DiagnosticSeverityError:
		return 0
	case domain.DiagnosticSeverityWarning:
		return 1
	case domain.DiagnosticSeverityInfo:
		return 2
	case domain.DiagnosticSeverityHint:
		return 3
	default:
		return 4
	}
}

func semanticSnapshotID(store repository.Store, snapshot overlay.OverlaySnapshot) domain.SnapshotID {
	if snapshot.BaseSnapshotID != "" {
		return snapshot.BaseSnapshotID
	}
	if store != nil {
		return store.SnapshotID()
	}
	return ""
}

func semanticViewHash(snapshot overlay.OverlaySnapshot, parseSnapshot parsesession.Snapshot) string {
	return semanticHash("view", string(snapshot.DocumentID), fmt.Sprintf("%d", snapshot.Version), snapshot.ContentHash, parseSnapshot.ContentHash, fmt.Sprintf("%d", parseSnapshot.TreeVersion))
}

func semanticHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func boundDiagnosticMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > maxDiagnosticMessage {
		return textutil.TruncateUTF8(message, maxDiagnosticMessage) + "..."
	}
	return message
}

func normalizeParseSessionError(documentID string, err error) error {
	if errors.Is(err, parsesession.ErrSessionNotFound) {
		return apperror.DocumentNotFound(documentID)
	}
	if errors.Is(err, parsesession.ErrVersionUnavailable) {
		return apperror.DocumentVersionConflict("")
	}
	return apperror.InternalError(err)
}
