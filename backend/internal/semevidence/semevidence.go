// Package semevidence bridges a SemanticProvider (gopls, the TypeScript server)
// to the ContextPacker: it collects the methods a policy requested over the same
// pinned view, converts facts into provenance-tagged ContextPack evidence, and
// reports unavailability/limits as omissions — never as a silent empty slice. It
// implements contextpack.SemanticSource.
package semevidence

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// Collector gathers semantic evidence for a context pack.
type Collector struct {
	provider semantic.SemanticProvider
	deadline time.Duration
	maxFacts int
}

// NewCollector builds a collector. deadline bounds the whole collection; maxFacts
// caps the total evidence returned.
func NewCollector(provider semantic.SemanticProvider, deadline time.Duration, maxFacts int) *Collector {
	if deadline <= 0 {
		deadline = 4 * time.Second
	}
	if maxFacts <= 0 {
		maxFacts = 24
	}
	return &Collector{provider: provider, deadline: deadline, maxFacts: maxFacts}
}

type methodFn func(context.Context, semantic.SemanticQuery) ([]semantic.SemanticFact, error)

// Collect runs the requested methods and returns evidence + omissions. A
// mandatory provider that is unavailable returns an error; otherwise unavailability
// is an omission (ast_only coverage).
func (c *Collector) Collect(ctx context.Context, request contextpack.SemanticRequest) (contextpack.SemanticResult, error) {
	query := queryFrom(request)
	caps, err := c.capabilities(ctx, query.Path)
	if err != nil || isDisabled(caps) {
		if request.Mandatory && err != nil {
			return contextpack.SemanticResult{}, fmt.Errorf("semevidence: mandatory provider unavailable: %w", err)
		}
		reason := "disabled"
		if err != nil {
			reason = "unavailable"
		}
		return contextpack.SemanticResult{Omissions: []contextpack.Omission{{
			Reason: contextpack.OmitSourceUnavailable, Ref: "semantic:" + reason, Detail: "semantic coverage ast_only",
		}}}, nil
	}

	methods := c.dispatch(caps)

	collectCtx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()

	type outcome struct {
		method string
		facts  []semantic.SemanticFact
		err    error
	}
	var wg sync.WaitGroup
	results := make([]outcome, len(request.Methods))
	for i, method := range request.Methods {
		if preloaded, ok := request.Preloaded[method]; ok {
			results[i] = outcome{method: method, facts: preloaded.Facts, err: preloaded.Err}
			continue
		}
		fn, ok := methods[method]
		if !ok {
			results[i] = outcome{method: method, err: semantic.CapabilityUnsupported(method)}
			continue
		}
		wg.Add(1)
		go func(index int, name string, run methodFn) {
			defer wg.Done()
			facts, runErr := run(collectCtx, query)
			results[index] = outcome{method: name, facts: facts, err: runErr}
		}(i, method, fn)
	}
	wg.Wait()

	result := contextpack.SemanticResult{}
	for _, item := range results {
		if item.err != nil {
			result.Omissions = append(result.Omissions, contextpack.Omission{
				Reason: contextpack.OmitUnsupported, Ref: "semantic:" + item.method, Detail: "method unavailable",
			})
			continue
		}
		for _, fact := range item.facts {
			if len(result.Evidence) >= c.maxFacts {
				result.Omissions = append(result.Omissions, contextpack.Omission{Reason: contextpack.OmitBudget, Ref: "semantic", Detail: "semantic evidence truncated"})
				break
			}
			result.Evidence = append(result.Evidence, evidenceFrom(fact, item.method))
		}
	}
	sortEvidence(result.Evidence)
	return result, nil
}

type pathCapabilitiesProvider interface {
	CapabilitiesForPath(context.Context, string) (semantic.SemanticCapabilities, error)
}

func (c *Collector) capabilities(ctx context.Context, path string) (semantic.SemanticCapabilities, error) {
	if provider, ok := c.provider.(pathCapabilitiesProvider); ok {
		return provider.CapabilitiesForPath(ctx, path)
	}
	return c.provider.Capabilities(ctx)
}

func (c *Collector) dispatch(_ semantic.SemanticCapabilities) map[string]methodFn {
	return map[string]methodFn{
		"hover":          c.provider.Hover,
		"definition":     c.provider.Definitions,
		"references":     c.provider.References,
		"implementation": c.provider.Implementations,
		"incomingCalls":  c.provider.IncomingCalls,
		"outgoingCalls":  c.provider.OutgoingCalls,
		"diagnostics":    c.provider.Diagnostics,
	}
}

func isDisabled(caps semantic.SemanticCapabilities) bool {
	return !caps.Hover && !caps.Definition && !caps.References && !caps.Implementation && !caps.CallHierarchy && !caps.Diagnostics
}

func queryFrom(request contextpack.SemanticRequest) semantic.SemanticQuery {
	query := semantic.SemanticQuery{
		Path:            locationPath(request.Location),
		DocumentID:      request.DocumentID,
		DocumentVersion: request.DocumentVersion,
		Content:         request.Content,
	}
	if request.View != nil {
		query.SnapshotID = request.View.Metadata().ID
	}
	if request.Location != nil {
		query.Position = domain.Position{Line: request.Location.Line, Column: request.Location.Column}
		query.SymbolID = request.Location.SymbolID
	}
	return query
}

func locationPath(location *contextpack.SourceLocation) string {
	if location == nil {
		return ""
	}
	return location.Path
}

// evidenceFrom converts a semantic fact to ContextPack evidence: the kind is an
// LSP fact, content is the bounded detail (kept separate from instructions), and
// provenance/confidence are preserved. The deterministic ID is stamped by Finalize.
func evidenceFrom(fact semantic.SemanticFact, method string) contextpack.Evidence {
	return contextpack.SemanticEvidenceFromFact(fact, method)
}

func sortEvidence(evidence []contextpack.Evidence) {
	sort.SliceStable(evidence, func(i, j int) bool {
		if evidence[i].Relevance != evidence[j].Relevance {
			return evidence[i].Relevance > evidence[j].Relevance
		}
		if evidence[i].Path != evidence[j].Path {
			return evidence[i].Path < evidence[j].Path
		}
		return evidence[i].Range.Start.Line < evidence[j].Range.Start.Line
	})
}
