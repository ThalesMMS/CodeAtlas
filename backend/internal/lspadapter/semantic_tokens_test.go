package lspadapter

import (
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
)

var semanticTokenModifierSink []string

func TestDecodeSemanticTokensOmitsUnconvertibleRange(t *testing.T) {
	source := []byte("alpha beta")
	result := &lspfacts.SemanticTokens{Data: []uint32{
		0, 0, 5, 0, 0,
		0, 6, 100, 0, 0,
	}}
	legend := lspfacts.SemanticTokensLegend{TokenTypes: []string{"variable"}}

	tokens, truncated, omittedCount, err := DecodeSemanticTokens(
		source,
		lspconv.LineStarts(source),
		lspconv.EncodingUTF16,
		result,
		legend,
		nil,
	)
	if err != nil {
		t.Fatalf("DecodeSemanticTokens() error = %v", err)
	}
	if truncated {
		t.Fatal("DecodeSemanticTokens() truncated = true, want false")
	}
	if omittedCount != 1 {
		t.Fatalf("DecodeSemanticTokens() omittedCount = %d, want 1", omittedCount)
	}
	if len(tokens) != 1 || tokens[0].TokenType != "variable" {
		t.Fatalf("DecodeSemanticTokens() tokens = %#v, want the valid variable token", tokens)
	}
}

func TestCanonicalTokenModifiersZeroBitsDoesNotAllocate(t *testing.T) {
	if modifiers := canonicalTokenModifiers(0, []string{"declaration"}); modifiers != nil {
		t.Fatalf("canonicalTokenModifiers(0) = %#v, want nil", modifiers)
	}

	allocations := testing.AllocsPerRun(1000, func() {
		semanticTokenModifierSink = canonicalTokenModifiers(0, []string{"declaration"})
	})
	if allocations != 0 {
		t.Fatalf("canonicalTokenModifiers(0) allocations = %v, want 0", allocations)
	}
}
