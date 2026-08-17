package gopls

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// Call-hierarchy limits.
const (
	maxCallHierarchyItems = 8
	maxCalls              = 100
	maxRangesPerRelation  = 16
)

func capCallHierarchy(c semantic.SemanticCapabilities) bool { return c.CallHierarchy }

// prepareItem runs prepareCallHierarchy and selects the single item matching the
// target. Multiple distinct items are SYMBOL_AMBIGUOUS (never chosen by index).
func (p *Provider) prepareItem(ctx context.Context, query semantic.SemanticQuery) (lspfacts.CallHierarchyItem, prepared, error) {
	prep, err := p.prepare(query, capCallHierarchy, "textDocument/prepareCallHierarchy")
	if err != nil {
		return lspfacts.CallHierarchyItem{}, prepared{}, err
	}
	var items []lspfacts.CallHierarchyItem
	if err := prep.client.Request(ctx, "textDocument/prepareCallHierarchy", positionParams(prep), &items); err != nil {
		return lspfacts.CallHierarchyItem{}, prepared{}, err
	}
	if len(items) > maxCallHierarchyItems {
		items = items[:maxCallHierarchyItems]
	}
	if len(items) == 0 {
		return lspfacts.CallHierarchyItem{}, prep, errNoCallHierarchyItem
	}
	if len(items) > 1 && !lspadapter.SameSymbol(items) {
		return lspfacts.CallHierarchyItem{}, prep, apperror.SymbolAmbiguous(items[0].Name, len(items))
	}
	return items[0], prep, nil
}

var errNoCallHierarchyItem = fmt.Errorf("gopls: no call-hierarchy item at position")

// IncomingCalls returns caller→target facts (one hop). Depth expansion is the
// ContextPacker's job; the adapter never does hidden BFS.
func (p *Provider) IncomingCalls(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	item, prep, err := p.prepareItem(ctx, query)
	if err != nil {
		if err == errNoCallHierarchyItem {
			return nil, nil
		}
		return nil, err
	}
	var calls []lspfacts.IncomingCall
	if err := prep.client.Request(ctx, "callHierarchy/incomingCalls", map[string]any{"item": item}, &calls); err != nil {
		return nil, err
	}
	facts := make([]semantic.SemanticFact, 0, len(calls))
	converter := p.locationConverter(query, &prep)
	truncated := false
	for i, call := range calls {
		if i >= maxCalls {
			truncated = true
			break
		}
		// caller is `from`; the direction is caller → target (the query symbol).
		facts = append(facts, p.callFact(query, converter, semantic.KindCallIncoming, "callHierarchy/incomingCalls", call.From, call.From.URI, call.FromRanges))
	}
	return p.finishCalls(facts, truncated), nil
}

// OutgoingCalls returns target→callee facts (one hop).
func (p *Provider) OutgoingCalls(ctx context.Context, query semantic.SemanticQuery) ([]semantic.SemanticFact, error) {
	item, prep, err := p.prepareItem(ctx, query)
	if err != nil {
		if err == errNoCallHierarchyItem {
			return nil, nil
		}
		return nil, err
	}
	var calls []lspfacts.OutgoingCall
	if err := prep.client.Request(ctx, "callHierarchy/outgoingCalls", map[string]any{"item": item}, &calls); err != nil {
		return nil, err
	}
	facts := make([]semantic.SemanticFact, 0, len(calls))
	converter := p.locationConverter(query, &prep)
	truncated := false
	for i, call := range calls {
		if i >= maxCalls {
			truncated = true
			break
		}
		facts = append(facts, p.callFact(query, converter, semantic.KindCallOutgoing, "callHierarchy/outgoingCalls", call.To, item.URI, call.FromRanges))
	}
	return p.finishCalls(facts, truncated), nil
}

// callFact builds a directional call fact. The other end (caller/callee) is the
// Object location; valid fromRanges are kept as related evidence (bounded).
func (p *Provider) callFact(query semantic.SemanticQuery, converter *lspadapter.LocationConverter, kind, method string, other lspfacts.CallHierarchyItem, rangeURI string, fromRanges []lspfacts.Range) semantic.SemanticFact {
	location, _ := converter.Convert(lspfacts.Location{URI: other.URI, Range: lspfacts.SelectionOf(other)})
	fact := p.fact(query, kind, method, location, "")
	fact.Object = &semantic.SymbolRef{Name: other.Name}
	// Keep the valid call-site ranges as related evidence, bounded per relation.
	for i, r := range fromRanges {
		if i >= maxRangesPerRelation {
			break
		}
		if related, ok := converter.Convert(lspfacts.Location{URI: rangeURI, Range: r}); ok {
			fact.Related = append(fact.Related, related)
		}
	}
	return fact
}

// finishCalls dedupes by the other-end location, orders deterministically, and
// records truncation.
func (p *Provider) finishCalls(facts []semantic.SemanticFact, truncated bool) []semantic.SemanticFact {
	facts = lspadapter.FinishFacts(facts)
	if truncated && len(facts) > 0 {
		facts[len(facts)-1].Detail = semantic.SanitizeDetail(fmt.Sprintf("calls truncated at %d", maxCalls))
	}
	return facts
}
