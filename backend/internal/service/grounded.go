package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// packAllowSet is the set of EvidenceIDs the model is permitted to cite — exactly
// the pack's evidence, nothing else.
func packAllowSet(pack contextpack.ContextPack) map[string]struct{} {
	return aiout.AllowSet(packEvidenceIDs(pack))
}

func packEvidenceIDs(pack contextpack.ContextPack) []string {
	ids := make([]string, 0, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		ids = append(ids, string(evidence.ID))
	}
	return ids
}

// packResolver maps an EvidenceID to a backend-owned source reference, so a
// citation can only point at evidence that actually exists in the pack.
func packResolver(pack contextpack.ContextPack) aiout.Resolver {
	byID := make(map[string]contextpack.Evidence, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		byID[string(evidence.ID)] = evidence
	}
	return func(id string) (aiout.SourceReference, bool) {
		evidence, ok := byID[id]
		if !ok {
			return aiout.SourceReference{}, false
		}
		if evidence.Path == "" {
			return aiout.SourceReference{Label: evidence.Title}, true
		}
		return aiout.SourceReference{
			Label:     sourceLocationLabel(evidence.Path, evidence.Range.Start.Line, evidence.Range.End.Line),
			Path:      evidence.Path,
			StartLine: evidence.Range.Start.Line,
			EndLine:   evidence.Range.End.Line,
		}, true
	}
}

func packRelevantFiles(pack contextpack.ContextPack) []aiout.SourceReference {
	refs := make([]aiout.SourceReference, 0, len(pack.Evidence))
	seen := make(map[string]struct{}, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		if evidence.Path == "" {
			continue
		}
		if _, duplicate := seen[evidence.Path]; duplicate {
			continue
		}
		seen[evidence.Path] = struct{}{}
		refs = append(refs, aiout.SourceReference{Label: evidence.Path, Path: evidence.Path})
	}
	return refs
}

func packCodeResolver(pack contextpack.ContextPack) aiout.CodeResolver {
	byID := make(map[string]contextpack.Evidence, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		byID[string(evidence.ID)] = evidence
	}
	return func(id string) (aiout.CodeBlock, bool) {
		evidence, ok := byID[id]
		if !ok || evidence.Path == "" || evidence.DisplayCode == "" {
			return aiout.CodeBlock{}, false
		}
		return aiout.CodeBlock{
			Reference: aiout.SourceReference{
				Label: sourceLocationLabel(evidence.Path, evidence.Range.Start.Line, evidence.Range.End.Line),
				Path:  evidence.Path, StartLine: evidence.Range.Start.Line, EndLine: evidence.Range.End.Line,
			},
			Language: evidence.DisplayLanguage, Content: evidence.DisplayCode, Truncated: evidence.DisplayCodeTruncated,
		}, true
	}
}

func partitionExplanationCode(pack contextpack.ContextPack, ids []string) (definitions, usages []string) {
	byID := make(map[string]contextpack.Evidence, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		byID[string(evidence.ID)] = evidence
	}
	for _, id := range ids {
		if byID[id].Relation == contextpack.RelationUsageSite {
			usages = append(usages, id)
		} else {
			definitions = append(definitions, id)
		}
	}
	return definitions, usages
}

type seeAlsoCandidate struct {
	priority  int
	relevance float64
	ref       aiout.SourceReference
}

func packSeeAlso(pack contextpack.ContextPack, view repository.ReadView, targetID string, limit int) []aiout.SourceReference {
	if limit <= 0 {
		return nil
	}
	candidates := make([]seeAlsoCandidate, 0, len(pack.Evidence))
	seen := make(map[string]struct{})
	for _, evidence := range pack.Evidence {
		id := string(evidence.SymbolID)
		if id == "" || id == targetID || evidence.Kind == contextpack.KindTest || evidence.Relation == contextpack.RelationUsageSite || evidence.Path == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		symbol, ok := view.GetSymbol(id)
		if !ok || symbol.Kind == domain.KindFile || symbol.Kind == domain.KindImport || symbol.Kind == domain.KindTest {
			continue
		}
		seen[id] = struct{}{}
		candidates = append(candidates, seeAlsoCandidate{
			priority: seeAlsoPriority(symbol), relevance: evidence.Relevance,
			ref: aiout.SourceReference{
				Label: symbol.Name, Path: evidence.Path,
				StartLine: evidence.Range.Start.Line, EndLine: evidence.Range.End.Line,
			},
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		if candidates[i].relevance != candidates[j].relevance {
			return candidates[i].relevance > candidates[j].relevance
		}
		if candidates[i].ref.Label != candidates[j].ref.Label {
			return candidates[i].ref.Label < candidates[j].ref.Label
		}
		return candidates[i].ref.Path < candidates[j].ref.Path
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	refs := make([]aiout.SourceReference, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.ref)
	}
	return refs
}

func seeAlsoPriority(symbol domain.Symbol) int {
	switch symbol.Kind {
	case domain.KindInterface:
		return 0
	case domain.KindType, domain.KindClass:
		return 1
	case domain.KindFunction:
		if strings.HasPrefix(symbol.Name, "New") {
			return 2
		}
		return 4
	case domain.KindMethod:
		return 3
	default:
		return 4
	}
}

func validateCodeSelections(pack contextpack.ContextPack, ids []string) error {
	renderable := make(map[string]struct{}, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		if evidence.Path != "" && evidence.DisplayCode != "" {
			renderable[string(evidence.ID)] = struct{}{}
		}
	}
	var problems []string
	for _, id := range ids {
		if _, ok := renderable[id]; !ok {
			problems = append(problems, fmt.Sprintf("codeEvidenceIds references non-renderable evidence ID %q", id))
		}
	}
	if len(problems) > 0 {
		return &aiout.ValidationError{Problems: problems}
	}
	return nil
}

func sourceLocationLabel(path string, startLine, endLine int) string {
	if startLine <= 0 {
		return path
	}
	if endLine > startLine {
		return fmt.Sprintf("%s:%d-%d", path, startLine, endLine)
	}
	return fmt.Sprintf("%s:%d", path, startLine)
}

// generateGrounded runs one structured generation with at most one controlled
// retry. No partial output is returned.
func generateGrounded(ctx context.Context, provider ai.Provider, request ai.GenerationRequest, validate func([]byte) error) error {
	return generateGroundedWithAttempts(ctx, provider, request, 2, validate)
}

// generateGroundedWithAttempts validates every response and performs a bounded
// number of fresh corrections. Each correction keeps the original request and
// carries only a compact, content-free summary of the immediately preceding
// validation failure. Rejected model output is never copied into another prompt.
func generateGroundedWithAttempts(ctx context.Context, provider ai.Provider, request ai.GenerationRequest, maxAttempts int, validate func([]byte) error) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var validationErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		current := request
		if validationErr != nil {
			current.UserPrompt = request.UserPrompt + "\n\nThe previous response was rejected during validation. Correct it while keeping EXACTLY the same scope, the same task, and ONLY the same evidence. Problems:\n" + validationSummary(validationErr)
		}
		result, err := ai.Generate(ctx, provider, current)
		if err != nil {
			return classifyGenerationError(ctx, err)
		}
		validationErr = validate(result.RawJSON)
		if validationErr == nil {
			return nil
		}
	}
	// Preserve the violated rules for internal diagnostics, but redact every
	// quoted model-authored value before the cause reaches logs.
	return apperror.ModelOutputInvalid(fmt.Errorf(
		"operation %s rejected after correction: %s", request.Operation, validationDiagnosticSummary(validationErr),
	))
}

// classifyGenerationError preserves cancellation/deadline identity so the HTTP
// seam can distinguish a client-aborted request from a provider outage.
func classifyGenerationError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperror.ProviderUnavailable(err)
}

// validationSummary extracts the compact, content-free problem list.
type validationSummaryProvider interface {
	ValidationSummary() string
}

func validationSummary(err error) string {
	var verr *aiout.ValidationError
	if errors.As(err, &verr) {
		return verr.Summary()
	}
	var provider validationSummaryProvider
	if errors.As(err, &provider) {
		if summary := strings.TrimSpace(provider.ValidationSummary()); summary != "" {
			return summary
		}
	}
	return "invalid response"
}

// validationDiagnosticSummary retains field paths and violated rules while
// removing quoted values, which may have been authored by an untrusted model.
// The result is bounded before it is attached to an internal error cause.
func validationDiagnosticSummary(err error) string {
	summary := validationSummary(err)
	var builder strings.Builder
	inQuote := false
	escaped := false
	for _, char := range summary {
		if inQuote {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				builder.WriteString("<redacted>\"")
				inQuote = false
			}
			continue
		}
		if char == '"' {
			builder.WriteRune(char)
			inQuote = true
			continue
		}
		builder.WriteRune(char)
	}
	if inQuote {
		builder.WriteString("<redacted>")
	}
	const maxDiagnosticRunes = 1200
	runes := []rune(builder.String())
	if len(runes) > maxDiagnosticRunes {
		runes = append(runes[:maxDiagnosticRunes], '…')
	}
	return string(runes)
}
