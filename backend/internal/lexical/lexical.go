// Package lexical is the neutral, versioned identifier tokenizer shared by the
// in-memory lexical index and the SQLite FTS5 index. Keeping one source of truth
// guarantees both backends tokenize identifiers identically (camelCase / acronym /
// digit expansion, Unicode lower-casing, stop-word and short-token removal,
// deterministic order) so search results stay comparable. A version string is
// stamped into the FTS index metadata; an incompatible change forces an FTS rebuild
// and a policy/eval bump.
package lexical

import (
	"strings"
	"unicode"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// Version identifies the tokenizer contract. Bump on any incompatible change.
const Version = "lexical.v3"

// stopWords are removed from both documents and queries, matching the original
// in-memory index.
var stopWords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "this": {}, "that": {},
	"of": {}, "to": {}, "in": {}, "on": {}, "is": {}, "as": {}, "by": {},
}

// Tokenize splits on non-alphanumerics, expands each identifier into its semantic
// pieces, lower-cases (Unicode), and drops sub-2-rune tokens and stop words. Order
// is deterministic (input order, with duplicates preserved as term frequency).
func Tokenize(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	result := make([]string, 0, len(fields)*2)
	for _, field := range fields {
		for _, piece := range expandIdentifier(field) {
			token := strings.ToLower(piece)
			if len([]rune(token)) < 2 || isStopWord(token) {
				continue
			}
			result = append(result, token)
		}
	}
	return result
}

// Unique returns the tokens with duplicates removed, preserving first-seen order.
func Unique(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

// expandIdentifier indexes the complete identifier and its camelCase / PascalCase /
// acronym / digit pieces, so "submit order" can match SubmitOrder, submit_order.
func expandIdentifier(value string) []string {
	runes := []rune(value)
	if len(runes) == 0 {
		return nil
	}
	parts := []string{value}
	start := 0
	for index := 1; index < len(runes); index++ {
		previous, current := runes[index-1], runes[index]
		boundary := unicode.IsUpper(current) && unicode.IsLower(previous)
		if unicode.IsUpper(current) && unicode.IsUpper(previous) && index+1 < len(runes) && unicode.IsLower(runes[index+1]) {
			boundary = true
		}
		if unicode.IsDigit(current) != unicode.IsDigit(previous) {
			boundary = true
		}
		if !boundary {
			continue
		}
		parts = append(parts, string(runes[start:index]))
		start = index
	}
	if start > 0 {
		parts = append(parts, string(runes[start:]))
	}
	return uniqueFold(parts)
}

func uniqueFold(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func isStopWord(token string) bool {
	_, ok := stopWords[token]
	return ok
}

// Document holds the per-field token streams of a symbol. Field weighting is
// expressed by separate columns (FTS5 bm25 weights) instead of repeating text.
type Document struct {
	Name      []string
	Qualified []string
	Kind      []string
	Path      []string
	Signature []string
	Summary   []string
	Code      []string
}

// BuildDocument tokenizes a flat symbol field-by-field.
func BuildDocument(symbol domain.Symbol) Document {
	return Document{
		Name:      Tokenize(symbol.Name),
		Qualified: Tokenize(symbol.QualifiedName),
		Kind:      Tokenize(symbol.Kind),
		Path:      Tokenize(symbol.Path),
		Signature: Tokenize(symbol.Signature),
		Summary:   Tokenize(strings.Join([]string{symbol.DocComment, symbol.Summary}, " ")),
		Code:      Tokenize(symbol.Code),
	}
}

// Field column accessors join tokens with spaces for the FTS5 row.
func (d Document) NameColumn() string      { return strings.Join(d.Name, " ") }
func (d Document) QualifiedColumn() string { return strings.Join(d.Qualified, " ") }
func (d Document) KindColumn() string      { return strings.Join(d.Kind, " ") }
func (d Document) PathColumn() string      { return strings.Join(d.Path, " ") }
func (d Document) SignatureColumn() string { return strings.Join(d.Signature, " ") }
func (d Document) SummaryColumn() string   { return strings.Join(d.Summary, " ") }
func (d Document) CodeColumn() string      { return strings.Join(d.Code, " ") }

// BuildQuery returns the unique query tokens.
func BuildQuery(query string) []string {
	return Unique(Tokenize(query))
}

// MatchExpression builds a safe FTS5 MATCH expression from the query tokens —
// each token is a quoted string OR-ed together, so raw user input never reaches
// the FTS5 query parser. Returns "" when there are no usable tokens.
func MatchExpression(query string) string {
	tokens := BuildQuery(query)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		// Tokens are already letters/digits only; double any quote defensively.
		quoted = append(quoted, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
