package lspadapter

import (
	"sort"

	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

const MaxSemanticTokens = 20_000

// DecodeSemanticTokens validates and converts one negotiated relative-token
// stream into CodeAtlas' provider-neutral legend and internal byte ranges.
// Unknown provider types/modifiers and unconvertible buffer ranges are omitted;
// malformed, overlapping or out-of-order relative streams reject the whole
// payload.
func DecodeSemanticTokens(
	source []byte,
	starts []int,
	encoding string,
	result *lspfacts.SemanticTokens,
	legend lspfacts.SemanticTokensLegend,
	typeAliases map[string]string,
) (tokens []semantic.SemanticToken, truncated bool, omittedCount int, err error) {
	if result == nil {
		return []semantic.SemanticToken{}, false, 0, nil
	}
	if len(result.Data)%5 != 0 {
		return nil, false, 0, semantic.ErrProviderPayloadInvalid
	}
	rawCount := len(result.Data) / 5
	limit := rawCount
	if limit > MaxSemanticTokens {
		limit = MaxSemanticTokens
		truncated = true
		omittedCount = rawCount - limit
	}
	tokens = make([]semantic.SemanticToken, 0, limit)
	line, character := uint64(0), uint64(0)
	previousLine, previousEnd := uint64(0), uint64(0)
	hasPrevious := false
	for tokenIndex, offset := 0, 0; tokenIndex < limit; tokenIndex, offset = tokenIndex+1, offset+5 {
		deltaLine := uint64(result.Data[offset])
		deltaStart := uint64(result.Data[offset+1])
		length := uint64(result.Data[offset+2])
		typeIndex := uint64(result.Data[offset+3])
		modifierBits := uint64(result.Data[offset+4])
		if deltaLine > 0 {
			line += deltaLine
			character = deltaStart
		} else {
			character += deltaStart
		}
		if length == 0 || line > uint64(^uint(0)>>1) || character > uint64(^uint(0)>>1) ||
			character+length > uint64(^uint(0)>>1) || typeIndex >= uint64(len(legend.TokenTypes)) ||
			(hasPrevious && (line < previousLine || (line == previousLine && character < previousEnd))) {
			return nil, false, 0, semantic.ErrProviderPayloadInvalid
		}
		previousLine, previousEnd, hasPrevious = line, character+length, true
		tokenType, ok := canonicalTokenType(legend.TokenTypes[typeIndex], typeAliases)
		if !ok {
			continue
		}
		wireRange := lspfacts.Range{
			Start: lspfacts.Position{Line: int(line), Character: int(character)},
			End:   lspfacts.Position{Line: int(line), Character: int(character + length)},
		}
		rng, ok := ConvertRange(source, starts, wireRange, encoding)
		if !ok || rng.Start == rng.End {
			omittedCount++
			continue
		}
		tokens = append(tokens, semantic.SemanticToken{
			Range: rng, TokenType: tokenType,
			Modifiers: canonicalTokenModifiers(modifierBits, legend.TokenModifiers),
		})
	}
	return tokens, truncated, omittedCount, nil
}

func canonicalTokenType(providerType string, aliases map[string]string) (string, bool) {
	if alias := aliases[providerType]; alias != "" {
		providerType = alias
	}
	for _, canonical := range semantic.CanonicalSemanticTokenTypes {
		if providerType == canonical {
			return canonical, true
		}
	}
	return "", false
}

func canonicalTokenModifiers(bits uint64, legend []string) []string {
	if bits == 0 {
		return nil
	}
	modifiers := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for index, modifier := range legend {
		if index >= 64 || bits&(uint64(1)<<index) == 0 {
			continue
		}
		for _, canonical := range semantic.CanonicalSemanticTokenModifiers {
			if modifier == canonical {
				if _, exists := seen[canonical]; !exists {
					modifiers = append(modifiers, canonical)
					seen[canonical] = struct{}{}
				}
				break
			}
		}
	}
	sort.Strings(modifiers)
	return modifiers
}
