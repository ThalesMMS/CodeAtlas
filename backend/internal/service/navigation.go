package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/pathutil"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

type NavigationService struct {
	store     repository.Store
	workspace *Workspace
	provider  semantic.SemanticProvider
}

type NavigationViewOptions struct {
	ViewHash        string
	DocumentID      string
	DocumentVersion int64
}

type resolvedNavigationSubject struct {
	subject    domain.NavigationSubject
	symbol     *domain.Symbol
	candidates []domain.Symbol
}

func NewNavigationService(store repository.Store, workspace *Workspace) *NavigationService {
	return &NavigationService{store: store, workspace: workspace}
}

func (s *NavigationService) SetSemanticProvider(provider semantic.SemanticProvider) {
	if s != nil {
		s.provider = provider
	}
}

func (s *NavigationService) Query(ctx context.Context, request domain.NavigationRequest) (domain.NavigationResult, error) {
	if s == nil || s.store == nil {
		return domain.NavigationResult{}, apperror.InternalError(fmt.Errorf("navigation service not configured"))
	}
	normalized, err := normalizeNavigationRequest(request)
	if err != nil {
		return domain.NavigationResult{}, err
	}
	view, err := s.store.SnapshotContext(ctx)
	if err != nil {
		return domain.NavigationResult{}, err
	}
	defer view.Close()
	var source []byte
	if s.workspace != nil && navigationRequestNeedsSource(normalized) {
		if data, err := s.workspace.Read(normalized.Path); err == nil {
			source = data
		} else {
			return domain.NavigationResult{}, err
		}
	}
	return s.QueryOnView(ctx, normalized, view, source, NavigationViewOptions{})
}

func (s *NavigationService) QueryOnView(ctx context.Context, request domain.NavigationRequest, view repository.ReadView, source []byte, opts NavigationViewOptions) (domain.NavigationResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.NavigationResult{}, err
	}
	request, err := normalizeNavigationRequest(request)
	if err != nil {
		return domain.NavigationResult{}, err
	}
	resolved, err := s.resolveSubject(view, request, source)
	if err != nil {
		return domain.NavigationResult{}, err
	}
	metadata := view.Metadata()
	viewHash := opts.ViewHash
	if viewHash == "" {
		viewHash = string(metadata.ID)
	}
	coverage, omissions := s.navigationCoverage(request.Kind, "", "")
	result := domain.NavigationResult{
		Kind:             request.Kind,
		SnapshotID:       metadata.ID,
		ViewHash:         viewHash,
		DocumentID:       opts.DocumentID,
		DocumentVersion:  opts.DocumentVersion,
		Subject:          resolved.subject,
		Targets:          []domain.NavigationTarget{},
		SemanticCoverage: coverage,
		Omissions:        omissions,
	}

	var targets []domain.NavigationTarget
	switch request.Kind {
	case domain.NavigationKindDefinition:
		targets = queryDefinitionTargets(request.Kind, resolved)
	case domain.NavigationKindReferences:
		targets, err = queryReferenceTargets(view, resolved)
	case domain.NavigationKindImplementation:
		targets, err = queryImplementationTargets(view, resolved)
	case domain.NavigationKindIncomingCalls:
		targets, err = queryCallTargets(view, resolved, true)
	case domain.NavigationKindOutgoingCalls:
		targets, err = queryCallTargets(view, resolved, false)
	}
	if err != nil {
		return domain.NavigationResult{}, err
	}
	semanticTargets, semanticCoverage, semanticOmissions, err := s.querySemanticNavigation(ctx, request, resolved, metadata.ID, source, opts, view)
	if err != nil {
		return domain.NavigationResult{}, err
	}
	if request.Kind == domain.NavigationKindDefinition && len(semanticTargets) > 0 {
		targets = retainDefinitionFallbacksConfirmedBySemantic(targets, semanticTargets)
	}
	targets = append(targets, semanticTargets...)
	result.SemanticCoverage = semanticCoverage
	result.Omissions = semanticOmissions

	targets = dedupeNavigationTargets(targets)
	sortNavigationTargets(targets)
	result.Total = len(targets)
	limit := navigationLimit(request.Kind, request.Limit)
	if len(targets) > limit {
		result.Targets = append(result.Targets, targets[:limit]...)
		result.Truncated = true
		result.Omissions = append(result.Omissions, domain.NavigationOmission{
			Reason: "truncated", Ref: string(request.Kind), Detail: fmt.Sprintf("limited to %d targets", limit),
		})
	} else {
		result.Targets = append(result.Targets, targets...)
	}
	return result, nil
}

func normalizeNavigationRequest(request domain.NavigationRequest) (domain.NavigationRequest, error) {
	switch request.Kind {
	case domain.NavigationKindDefinition, domain.NavigationKindReferences, domain.NavigationKindImplementation,
		domain.NavigationKindIncomingCalls, domain.NavigationKindOutgoingCalls:
	default:
		return request, apperror.InvalidArgumentMessage("kind", "invalid navigation kind.", nil)
	}
	if request.Position != nil {
		if request.Position.Encoding != "" && request.Position.Encoding != "utf-16" {
			return request, apperror.InvalidArgumentMessage("position.encoding", "encoding must be utf-16.", nil)
		}
		request.Position.Encoding = "utf-16"
		if request.Position.Line <= 0 || request.Position.Column <= 0 {
			return request, apperror.InvalidArgumentMessage("position", "invalid position.", nil)
		}
	}
	hasTarget := request.SymbolID != "" || request.OccurrenceID != ""
	hasPosition := request.Path != "" && request.Position != nil
	if !hasTarget && !hasPosition {
		return request, apperror.InvalidArgumentMessage("target", "provide symbolId/occurrenceId or path+position.", nil)
	}
	return request, nil
}

func navigationRequestNeedsSource(request domain.NavigationRequest) bool {
	return request.Path != "" && request.Position != nil
}

func (s *NavigationService) resolveSubject(view repository.ReadView, request domain.NavigationRequest, source []byte) (resolvedNavigationSubject, error) {
	if request.SymbolID != "" {
		symbol, ok := view.GetSymbol(request.SymbolID)
		if !ok {
			return resolvedNavigationSubject{}, apperror.SymbolNotFound(request.Path, positionLine(request), positionColumn(request))
		}
		return exactNavigationSubject(symbol), nil
	}
	if request.OccurrenceID != "" {
		symbol, ok := navigationSymbolForOccurrence(view, request.OccurrenceID)
		if !ok {
			return resolvedNavigationSubject{}, apperror.SymbolNotFound(request.Path, positionLine(request), positionColumn(request))
		}
		return exactNavigationSubject(symbol), nil
	}

	name := ""
	if len(source) > 0 && request.Position != nil {
		name = IdentifierAt(source, request.Position.Line, request.Position.Column)
	}
	if name != "" {
		candidates := matchingNavigationSymbols(view, name, request.Path)
		if len(candidates) == 1 {
			return exactNavigationSubject(candidates[0]), nil
		}
		return resolvedNavigationSubject{
			subject:    domain.NavigationSubject{Name: name, Path: request.Path},
			candidates: candidates,
		}, nil
	}
	if request.Position != nil {
		if symbol, ok := view.SymbolAt(request.Path, request.Position.Line, request.Position.Column); ok {
			return exactNavigationSubject(symbol.ToSymbol()), nil
		}
	}
	return resolvedNavigationSubject{}, apperror.SymbolNotFound(request.Path, positionLine(request), positionColumn(request))
}

func exactNavigationSubject(symbol domain.Symbol) resolvedNavigationSubject {
	subject := navigationSubjectFromSymbol(symbol)
	return resolvedNavigationSubject{subject: subject, symbol: &symbol, candidates: []domain.Symbol{symbol}}
}

func navigationSubjectFromSymbol(symbol domain.Symbol) domain.NavigationSubject {
	return domain.NavigationSubject{
		SymbolID:      symbol.ID,
		OccurrenceID:  symbol.OccurrenceID,
		Path:          symbol.Path,
		Range:         symbol.Range,
		Name:          symbol.Name,
		QualifiedName: symbol.QualifiedName,
		Kind:          symbol.Kind,
	}
}

func matchingNavigationSymbols(view repository.ReadView, name, currentPath string) []domain.Symbol {
	candidates := view.SymbolsByName(name)
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := navigationResolutionScore(candidates[i], currentPath), navigationResolutionScore(candidates[j], currentPath)
		if left != right {
			return left > right
		}
		return navigationSymbolLess(candidates[i], candidates[j])
	})
	return candidates
}

func queryDefinitionTargets(kind domain.NavigationKind, resolved resolvedNavigationSubject) []domain.NavigationTarget {
	targets := make([]domain.NavigationTarget, 0, len(resolved.candidates))
	for _, symbol := range resolved.candidates {
		targets = append(targets, targetFromSymbol(kind, "definition", symbol, 0.82,
			[]domain.EvidenceProvenance{{Source: "tree-sitter", Detail: "symbol table"}}))
	}
	return targets
}

func queryReferenceTargets(view repository.ReadView, resolved resolvedNavigationSubject) ([]domain.NavigationTarget, error) {
	symbol, err := requireExactNavigationSymbol(resolved, "references")
	if err != nil {
		return nil, err
	}
	targets := make([]domain.NavigationTarget, 0)
	for _, edge := range view.EdgesForSymbol(symbol.ID) {
		if edge.ToSymbolID != symbol.ID || edge.Type == "contains" {
			continue
		}
		if source, ok := view.GetSymbol(edge.FromSymbolID); ok {
			targets = append(targets, targetFromEdgeSource(domain.NavigationKindReferences, "references", edge, source))
		}
	}
	return targets, nil
}

func queryImplementationTargets(view repository.ReadView, resolved resolvedNavigationSubject) ([]domain.NavigationTarget, error) {
	symbol, err := requireExactNavigationSymbol(resolved, "implementation")
	if err != nil {
		return nil, err
	}
	targets := make([]domain.NavigationTarget, 0)
	for _, edge := range view.EdgesForSymbol(symbol.ID) {
		if edge.Type != "implements" {
			continue
		}
		if edge.ToSymbolID == symbol.ID {
			if source, ok := view.GetSymbol(edge.FromSymbolID); ok {
				targets = append(targets, targetFromSymbol(domain.NavigationKindImplementation, "implementation", source, edge.Confidence,
					[]domain.EvidenceProvenance{{Source: "tree-sitter", Detail: "implements edge"}}))
			}
			continue
		}
		if edge.FromSymbolID == symbol.ID && edge.ToSymbolID != "" {
			if target, ok := view.GetSymbol(edge.ToSymbolID); ok {
				targets = append(targets, targetFromSymbol(domain.NavigationKindImplementation, "implementation", target, edge.Confidence,
					[]domain.EvidenceProvenance{{Source: "tree-sitter", Detail: "implements edge"}}))
			}
		}
	}
	return targets, nil
}

func queryCallTargets(view repository.ReadView, resolved resolvedNavigationSubject, incoming bool) ([]domain.NavigationTarget, error) {
	symbol, err := requireExactNavigationSymbol(resolved, "call hierarchy")
	if err != nil {
		return nil, err
	}
	targets := make([]domain.NavigationTarget, 0)
	for _, edge := range view.EdgesForSymbol(symbol.ID) {
		if edge.Type != "calls" {
			continue
		}
		if incoming {
			if edge.ToSymbolID != symbol.ID {
				continue
			}
			if source, ok := view.GetSymbol(edge.FromSymbolID); ok {
				targets = append(targets, targetFromEdgeSource(domain.NavigationKindIncomingCalls, "incoming_calls", edge, source))
			}
			continue
		}
		if edge.FromSymbolID != symbol.ID {
			continue
		}
		if edge.ToSymbolID != "" {
			if target, ok := view.GetSymbol(edge.ToSymbolID); ok {
				targets = append(targets, targetFromSymbol(domain.NavigationKindOutgoingCalls, "outgoing_calls", target, edge.Confidence,
					[]domain.EvidenceProvenance{{Source: "tree-sitter", Detail: "calls edge"}}))
				continue
			}
		}
		if edge.ToName != "" {
			targets = append(targets, externalNavigationTarget(domain.NavigationKindOutgoingCalls, "outgoing_calls", edge))
		}
	}
	return targets, nil
}

func requireExactNavigationSymbol(resolved resolvedNavigationSubject, field string) (domain.Symbol, error) {
	if resolved.symbol != nil {
		return *resolved.symbol, nil
	}
	if len(resolved.candidates) > 1 {
		return domain.Symbol{}, apperror.SymbolAmbiguous(resolved.subject.Name, len(resolved.candidates))
	}
	return domain.Symbol{}, apperror.InvalidArgumentMessage(field, field+" requires a resolved symbol.", nil)
}

func targetFromSymbol(kind domain.NavigationKind, relationship string, symbol domain.Symbol, confidence float64, provenance []domain.EvidenceProvenance) domain.NavigationTarget {
	target := domain.NavigationTarget{
		SymbolID:       symbol.ID,
		OccurrenceID:   symbol.OccurrenceID,
		Path:           internalNavigationPath(symbol.Path),
		Range:          normalizeNavigationRange(symbol.Range),
		SelectionRange: normalizeNavigationRange(symbol.Range),
		Label:          navigationLabel(symbol),
		Kind:           symbol.Kind,
		Relationship:   relationship,
		Provenance:     provenance,
		Confidence:     normalizedConfidence(confidence, 0.82),
		External:       !isInternalNavigationPath(symbol.Path),
	}
	if target.External {
		target.Path = ""
	}
	target.TargetID = navigationTargetID(kind, target)
	return target
}

func targetFromEdgeSource(kind domain.NavigationKind, relationship string, edge domain.Edge, source domain.Symbol) domain.NavigationTarget {
	rng := edgeRange(edge)
	target := domain.NavigationTarget{
		SymbolID:       source.ID,
		OccurrenceID:   source.OccurrenceID,
		Path:           internalNavigationPath(edge.Path),
		Range:          rng,
		SelectionRange: rng,
		Label:          navigationLabel(source),
		Kind:           source.Kind,
		Relationship:   relationship,
		Provenance: []domain.EvidenceProvenance{
			{Source: "tree-sitter", Detail: edge.Type + " edge"},
		},
		Confidence: normalizedConfidence(edge.Confidence, 0.72),
		External:   !isInternalNavigationPath(edge.Path),
	}
	if target.Path == "" && isInternalNavigationPath(source.Path) {
		target.Path = source.Path
	}
	if target.External {
		target.Path = ""
	}
	target.TargetID = navigationTargetID(kind, target)
	return target
}

func externalNavigationTarget(kind domain.NavigationKind, relationship string, edge domain.Edge) domain.NavigationTarget {
	target := domain.NavigationTarget{
		Label:        edge.ToName,
		Relationship: relationship,
		Provenance: []domain.EvidenceProvenance{
			{Source: "tree-sitter", Detail: edge.Type + " edge"},
		},
		Confidence: normalizedConfidence(edge.Confidence, 0.4),
		External:   true,
	}
	target.TargetID = navigationTargetID(kind, target)
	return target
}

func navigationSymbolForOccurrence(view repository.ReadView, occurrenceID string) (domain.Symbol, bool) {
	return view.GetSymbolByOccurrence(occurrenceID)
}

func dedupeNavigationTargets(targets []domain.NavigationTarget) []domain.NavigationTarget {
	seen := make(map[string]int, len(targets))
	out := targets[:0]
	for _, target := range targets {
		if index, ok := matchingDefinitionTarget(out, target); ok {
			out[index] = mergeNavigationTarget(out[index], target)
			continue
		}
		keyParts := []string{
			target.Relationship, target.Path,
			fmt.Sprintf("%d:%d:%d:%d", target.Range.Start.Line, target.Range.Start.Column, target.Range.End.Line, target.Range.End.Column),
			fmt.Sprintf("%t", target.External),
		}
		if target.Path == "" {
			keyParts = append(keyParts, target.Label)
		}
		key := strings.Join(keyParts, "\x00")
		if index, ok := seen[key]; ok {
			out[index] = mergeNavigationTarget(out[index], target)
			continue
		}
		seen[key] = len(out)
		out = append(out, target)
	}
	return out
}

func retainDefinitionFallbacksConfirmedBySemantic(fallbacks, semanticTargets []domain.NavigationTarget) []domain.NavigationTarget {
	confirmed := fallbacks[:0]
	for _, fallback := range fallbacks {
		for _, semanticTarget := range semanticTargets {
			sameIdentity := fallback.SymbolID != "" && fallback.SymbolID == semanticTarget.SymbolID
			sameLocation := fallback.Path == semanticTarget.Path && fallback.Range == semanticTarget.Range
			if sameIdentity || sameLocation {
				confirmed = append(confirmed, fallback)
				break
			}
		}
	}
	return confirmed
}

func matchingDefinitionTarget(targets []domain.NavigationTarget, candidate domain.NavigationTarget) (int, bool) {
	if candidate.Relationship != "definition" || candidate.SymbolID == "" {
		return 0, false
	}
	for index, target := range targets {
		if target.Relationship == candidate.Relationship && target.Path == candidate.Path &&
			target.SymbolID == candidate.SymbolID && navigationRangesOverlap(target.Range, candidate.Range) {
			return index, true
		}
	}
	return 0, false
}

func navigationRangesOverlap(left, right domain.Range) bool {
	return navigationPositionLessOrEqual(left.Start, right.End) && navigationPositionLessOrEqual(right.Start, left.End)
}

func navigationPositionLessOrEqual(left, right domain.Position) bool {
	return left.Line < right.Line || left.Line == right.Line && left.Column <= right.Column
}

func mergeNavigationTarget(current, addition domain.NavigationTarget) domain.NavigationTarget {
	if addition.Confidence > current.Confidence {
		current, addition = addition, current
	}
	if current.SymbolID == "" {
		current.SymbolID = addition.SymbolID
	}
	if current.OccurrenceID == "" {
		current.OccurrenceID = addition.OccurrenceID
	}
	if current.Label == "" {
		current.Label = addition.Label
	}
	if current.Kind == "" {
		current.Kind = addition.Kind
	}
	seen := make(map[string]struct{}, len(current.Provenance)+len(addition.Provenance))
	for _, provenance := range current.Provenance {
		seen[provenance.Source+"\x00"+provenance.Detail] = struct{}{}
	}
	for _, provenance := range addition.Provenance {
		key := provenance.Source + "\x00" + provenance.Detail
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		current.Provenance = append(current.Provenance, provenance)
	}
	return current
}

func sortNavigationTargets(targets []domain.NavigationTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.Relationship != b.Relationship {
			return a.Relationship < b.Relationship
		}
		if a.External != b.External {
			return !a.External
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		if a.Range.Start.Column != b.Range.Start.Column {
			return a.Range.Start.Column < b.Range.Start.Column
		}
		if a.SymbolID != b.SymbolID {
			return a.SymbolID < b.SymbolID
		}
		return a.TargetID < b.TargetID
	})
}

func navigationLimit(kind domain.NavigationKind, requested int) int {
	maximum := 100
	switch kind {
	case domain.NavigationKindDefinition:
		maximum = 20
	case domain.NavigationKindImplementation:
		maximum = 50
	case domain.NavigationKindReferences:
		maximum = 500
	case domain.NavigationKindIncomingCalls, domain.NavigationKindOutgoingCalls:
		maximum = 200
	}
	if requested <= 0 || requested > maximum {
		return maximum
	}
	return requested
}

func (s *NavigationService) querySemanticNavigation(
	ctx context.Context,
	request domain.NavigationRequest,
	resolved resolvedNavigationSubject,
	snapshotID domain.SnapshotID,
	source []byte,
	opts NavigationViewOptions,
	view repository.ReadView,
) ([]domain.NavigationTarget, map[string]any, []domain.NavigationOmission, error) {
	if s.provider == nil {
		coverage, omissions := s.navigationCoverage(request.Kind, "disabled", "tree-sitter")
		return nil, coverage, omissions, nil
	}

	path := request.Path
	position, hasPosition := navigationSemanticPosition(source, request.Position)
	if !hasPosition && request.Position == nil && resolved.symbol != nil {
		path = resolved.symbol.Path
		position = resolved.symbol.Range.Start
		position.Encoding = "utf-8"
		hasPosition = true
	}
	providerID := s.semanticProviderID(path)
	if path == "" || !hasPosition {
		coverage, omissions := s.navigationCoverage(request.Kind, "not_queried", providerID)
		return nil, coverage, omissions, nil
	}

	query := semantic.SemanticQuery{
		SnapshotID: snapshotID, DocumentID: semantic.DocumentID(opts.DocumentID), DocumentVersion: semantic.DocumentVersion(opts.DocumentVersion),
		Path: path, Position: position,
		SymbolID: domain.SymbolID(resolved.subject.SymbolID),
	}
	if opts.DocumentID != "" && path == request.Path {
		query.Content = source
	}
	facts, err := semanticNavigationFacts(ctx, s.provider, request.Kind, query)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, nil, err
		}
		state := "unavailable"
		if errors.Is(err, semantic.ErrCapabilityUnsupported) {
			state = "unsupported"
		}
		coverage, omissions := s.navigationCoverage(request.Kind, state, providerID)
		return nil, coverage, omissions, nil
	}

	targets := make([]domain.NavigationTarget, 0, len(facts))
	for _, fact := range facts {
		targets = append(targets, s.navigationTargetFromSemanticFact(request.Kind, fact, resolved, source, path, view))
	}
	coverage, _ := s.navigationCoverage(request.Kind, "available", providerID)
	return targets, coverage, nil, nil
}

func navigationSemanticPosition(source []byte, position *domain.NavigationPosition) (domain.Position, bool) {
	if position == nil || len(source) == 0 {
		return domain.Position{}, false
	}
	line, column, err := lspconv.LSPToInternal(
		source, lspconv.LineStarts(source), position.Line-1, position.Column-1, lspconv.EncodingUTF16,
	)
	if err != nil {
		return domain.Position{}, false
	}
	return domain.Position{Line: line, Column: column, Encoding: "utf-8"}, true
}

func semanticNavigationFacts(ctx context.Context, provider semantic.SemanticProvider, kind domain.NavigationKind, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	switch kind {
	case domain.NavigationKindDefinition:
		return provider.Definitions(ctx, query)
	case domain.NavigationKindReferences:
		return provider.References(ctx, query)
	case domain.NavigationKindImplementation:
		return provider.Implementations(ctx, query)
	case domain.NavigationKindIncomingCalls:
		return provider.IncomingCalls(ctx, query)
	case domain.NavigationKindOutgoingCalls:
		return provider.OutgoingCalls(ctx, query)
	default:
		return nil, semantic.CapabilityUnsupported(string(kind))
	}
}

func (s *NavigationService) navigationTargetFromSemanticFact(kind domain.NavigationKind, fact semantic.SemanticFact, resolved resolvedNavigationSubject, querySource []byte, queryPath string, view repository.ReadView) domain.NavigationTarget {
	path := internalNavigationPath(fact.Location.Path)
	rng := s.semanticNavigationRange(fact.Location, querySource, queryPath)
	label := ""
	targetKind := ""
	symbolID := ""
	occurrenceID := ""
	if fact.Object != nil {
		if fact.Object.Name != "" {
			label = fact.Object.Name
		}
		symbolID = string(fact.Object.SymbolID)
	}
	if path != "" {
		lookup := fact.Location.Range.Start
		if fact.Location.Encoding != semantic.EncodingUTF8 {
			lookup = rng.Start
		}
		if symbol, ok := view.SymbolAt(path, lookup.Line, lookup.Column); ok {
			flat := symbol.ToSymbol()
			expectedName := label
			if expectedName == "" && (kind == domain.NavigationKindDefinition || kind == domain.NavigationKindReferences) {
				expectedName = resolved.subject.Name
			}
			if (expectedName == "" || flat.Name == expectedName) && s.semanticLocationMatchesIndexedSymbol(flat, path, rng, querySource, queryPath) {
				if label == "" {
					label = navigationLabel(flat)
				}
				if symbolID == "" {
					symbolID = flat.ID
				}
				occurrenceID = flat.OccurrenceID
				targetKind = flat.Kind
			}
		}
	}
	if label == "" {
		label = resolved.subject.Name
	}
	if label == "" {
		label = filepath.Base(fact.Location.Path)
	}
	target := domain.NavigationTarget{
		SymbolID: symbolID, OccurrenceID: occurrenceID, Path: path, Range: rng, SelectionRange: rng,
		Label: label, Kind: targetKind, Relationship: string(kind),
		Provenance: []domain.EvidenceProvenance{{Source: fact.Provenance.Source, Detail: fact.Provenance.Method}},
		Confidence: normalizedConfidence(fact.Confidence, semantic.ConfidenceLanguageServerResolved),
		External:   path == "",
	}
	target.TargetID = navigationTargetID(kind, target)
	return target
}

func (s *NavigationService) semanticLocationMatchesIndexedSymbol(symbol domain.Symbol, path string, rng domain.Range, querySource []byte, queryPath string) bool {
	if internalNavigationPath(symbol.Path) != path || symbol.Name == "" || rng.Start.Line != symbol.Range.Start.Line {
		return false
	}
	source, ok := s.semanticNavigationSource(path, querySource, queryPath)
	if !ok {
		return false
	}
	name, nameRange, ok := IdentifierAtWithRange(source, rng.Start.Line, rng.Start.Column)
	if !ok || name != symbol.Name {
		return false
	}
	convertedNameRange := domain.Range{
		Start: byteColumnToUTF16(source, nameRange.Start),
		End:   byteColumnToUTF16(source, nameRange.End),
	}
	if convertedNameRange.Start.Line != rng.Start.Line || convertedNameRange.Start.Column != rng.Start.Column ||
		convertedNameRange.End.Line != rng.End.Line || convertedNameRange.End.Column != rng.End.Column {
		return false
	}
	lineStart, lineEnd := sourceLineBounds(source, rng.Start.Line-1)
	declarationStart := symbol.Range.Start.Column - 1
	if declarationStart < 0 {
		declarationStart = 0
	}
	line := source[lineStart:lineEnd]
	if declarationStart > len(line) {
		return false
	}
	headerEnd := len(line)
	if bodyStart := strings.IndexByte(string(line[declarationStart:]), '{'); bodyStart >= 0 {
		headerEnd = declarationStart + bodyStart
	}
	nameOffset := strings.Index(string(line[declarationStart:headerEnd]), symbol.Name)
	if nameOffset < 0 {
		return false
	}
	return declarationStart+nameOffset+1 == nameRange.Start.Column
}

func (s *NavigationService) semanticNavigationSource(path string, querySource []byte, queryPath string) ([]byte, bool) {
	if path == queryPath && len(querySource) > 0 {
		return querySource, true
	}
	if s.workspace == nil {
		return nil, false
	}
	source, err := s.workspace.Read(path)
	return source, err == nil
}

func (s *NavigationService) semanticNavigationRange(location semantic.SourceLocation, querySource []byte, queryPath string) domain.Range {
	rng := location.Range
	if location.Encoding != semantic.EncodingUTF8 {
		return normalizeNavigationRange(rng)
	}
	source, ok := s.semanticNavigationSource(location.Path, querySource, queryPath)
	if !ok {
		return normalizeNavigationRange(rng)
	}
	rng.Start = byteColumnToUTF16(source, rng.Start)
	rng.End = byteColumnToUTF16(source, rng.End)
	return rng
}

func byteColumnToUTF16(source []byte, position domain.Position) domain.Position {
	position.Encoding = "utf-16"
	if position.Line <= 0 || position.Column <= 0 {
		return position
	}
	start, end := sourceLineBounds(source, position.Line-1)
	byteCount := position.Column - 1
	if byteCount > end-start {
		byteCount = end - start
	}
	if byteCount < 0 {
		byteCount = 0
	}
	position.Column = utf16Column(string(source[start : start+byteCount]))
	return position
}

func (s *NavigationService) semanticProviderID(path string) string {
	if routed, ok := s.provider.(interface {
		ProviderIDForPath(string) (semantic.SemanticProviderID, bool)
	}); ok {
		if id, found := routed.ProviderIDForPath(path); found {
			return string(id)
		}
	}
	return string(s.provider.ID())
}

func (s *NavigationService) navigationCoverage(kind domain.NavigationKind, providerState, providerID string) (map[string]any, []domain.NavigationOmission) {
	if providerState == "" {
		if s.provider == nil {
			providerState = "disabled"
			providerID = "tree-sitter"
		} else {
			providerState = "not_queried"
			providerID = string(s.provider.ID())
		}
	}
	coverage := "ast_only"
	if providerState == "available" {
		coverage = "ast+lsp"
	}
	result := map[string]any{
		"coverage": coverage, "kind": string(kind), "providerState": providerState,
		"provider": providerID, "llm": false,
	}
	if providerState == "available" {
		return result, nil
	}
	reason := "provider_" + providerState
	detail := "semantic provider was not queried because path and position are unavailable"
	if providerState == "disabled" {
		detail = "semantic navigation provider is not configured"
	} else if providerState == "unsupported" {
		detail = "semantic provider does not support the requested navigation capability"
	} else if providerState == "unavailable" {
		detail = "semantic provider is unavailable for this request"
	}
	return result, []domain.NavigationOmission{{Reason: reason, Ref: "semantic_provider", Detail: detail}}
}

func navigationTargetID(kind domain.NavigationKind, target domain.NavigationTarget) string {
	raw := strings.Join([]string{
		string(kind), target.Relationship, target.SymbolID, target.OccurrenceID, target.Path,
		fmt.Sprintf("%d:%d:%d:%d", target.Range.Start.Line, target.Range.Start.Column, target.Range.End.Line, target.Range.End.Column),
		target.Label,
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "nav:" + hex.EncodeToString(sum[:12])
}

func navigationResolutionScore(symbol domain.Symbol, currentPath string) int {
	score := 0
	if symbol.Path == currentPath {
		score += 100
	}
	if filepath.Dir(symbol.Path) == filepath.Dir(currentPath) {
		score += 25
	}
	if symbol.Kind != "file" {
		score += 5
	}
	return score
}

func navigationSymbolLess(a, b domain.Symbol) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.Range.Start.Line != b.Range.Start.Line {
		return a.Range.Start.Line < b.Range.Start.Line
	}
	if a.Range.Start.Column != b.Range.Start.Column {
		return a.Range.Start.Column < b.Range.Start.Column
	}
	return a.ID < b.ID
}

func navigationLabel(symbol domain.Symbol) string {
	if symbol.Name != "" {
		return symbol.Name
	}
	if symbol.QualifiedName != "" {
		return symbol.QualifiedName
	}
	return symbol.ID
}

func isInternalNavigationPath(path string) bool {
	_, err := pathutil.NormalizeWorkspaceRelative(path)
	return err == nil
}

func internalNavigationPath(path string) string {
	normalized, err := pathutil.NormalizeWorkspaceRelative(path)
	if err != nil {
		return ""
	}
	return normalized
}

func edgeRange(edge domain.Edge) domain.Range {
	line := edge.Line
	if line <= 0 {
		line = 1
	}
	pos := domain.Position{Line: line, Column: 1, Encoding: "utf-16"}
	return domain.Range{Start: pos, End: pos}
}

func normalizeNavigationRange(rng domain.Range) domain.Range {
	rng.Start.Encoding = "utf-16"
	rng.End.Encoding = "utf-16"
	return rng
}

func normalizedConfidence(value, fallback float64) float64 {
	if value <= 0 {
		return fallback
	}
	if value > 1 {
		return 1
	}
	return value
}

func positionLine(request domain.NavigationRequest) int {
	if request.Position == nil {
		return 0
	}
	return request.Position.Line
}

func positionColumn(request domain.NavigationRequest) int {
	if request.Position == nil {
		return 0
	}
	return request.Position.Column
}
