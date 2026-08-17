package lexical

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func contains(tokens []string, want string) bool {
	for _, token := range tokens {
		if token == want {
			return true
		}
	}
	return false
}

func TestVersionIsStable(t *testing.T) {
	t.Parallel()
	if Version != "lexical.v3" {
		t.Fatalf("tokenizer version changed to %q; bump requires an FTS rebuild + eval update", Version)
	}
}

func TestTokenizeExpandsIdentifiers(t *testing.T) {
	t.Parallel()
	tokens := Tokenize("SubmitOrderService")
	for _, want := range []string{"submitorderservice", "submit", "order", "service"} {
		if !contains(tokens, want) {
			t.Errorf("expected %q in %v", want, tokens)
		}
	}
}

func TestTokenizeAcronymAndDigitBoundaries(t *testing.T) {
	t.Parallel()
	tokens := Tokenize("HTTPServer2")
	for _, want := range []string{"http", "server"} {
		if !contains(tokens, want) {
			t.Errorf("expected %q in %v", want, tokens)
		}
	}
}

func TestStopWordsAndShortTokensRemoved(t *testing.T) {
	t.Parallel()
	tokens := Tokenize("the and a order for with from this that of to in on is as by")
	if len(tokens) != 1 || tokens[0] != "order" {
		t.Fatalf("stop words / short tokens not filtered: %v", tokens)
	}
}

func TestTokenizeLowercasesUnicode(t *testing.T) {
	t.Parallel()
	tokens := Tokenize("ParseRÉSUMÉ")
	if !contains(tokens, "résumé") && !contains(tokens, "resume") {
		// unicode61 lower-casing keeps diacritics; we only assert lower-casing happened.
		if !contains(tokens, strings.ToLower("RÉSUMÉ")) {
			t.Fatalf("expected a lower-cased token in %v", tokens)
		}
	}
}

func TestBuildDocumentSeparatesFields(t *testing.T) {
	t.Parallel()
	doc := BuildDocument(domain.Symbol{Name: "PayService", QualifiedName: "pkg.PayService", Kind: "type", Path: "pkg/pay.go"})
	if !strings.Contains(doc.NameColumn(), "pay") || !strings.Contains(doc.NameColumn(), "service") {
		t.Fatalf("name column missing expanded tokens: %q", doc.NameColumn())
	}
	if doc.KindColumn() != "type" {
		t.Fatalf("kind column = %q", doc.KindColumn())
	}
}

func TestBuildDocumentIndexesDocCommentWithSummaryWeight(t *testing.T) {
	t.Parallel()
	doc := BuildDocument(domain.Symbol{DocComment: "Order is the checkout aggregate.", Summary: "type Order"})
	for _, want := range []string{"checkout", "aggregate", "order"} {
		if !contains(doc.Summary, want) {
			t.Fatalf("summary column missing doc-comment token %q: %v", want, doc.Summary)
		}
	}
}

func TestMatchExpressionIsSafe(t *testing.T) {
	t.Parallel()
	// FTS5 operators / quotes in raw input must not reach the parser unquoted.
	expr := MatchExpression(`Pay" OR rm -rf`)
	if expr == "" {
		t.Fatal("expected a non-empty MATCH expression")
	}
	if strings.Contains(expr, "rm") {
		// "rm" is 2 runes, a valid token, but must be quoted, not bare.
		if !strings.Contains(expr, `"rm"`) {
			t.Fatalf("token not quoted in %q", expr)
		}
	}
	// Every OR-joined piece must be a quoted string.
	for _, piece := range strings.Split(expr, " OR ") {
		if !strings.HasPrefix(piece, `"`) || !strings.HasSuffix(piece, `"`) {
			t.Fatalf("unquoted piece %q in %q", piece, expr)
		}
	}
}

func TestEmptyQueryHasNoMatch(t *testing.T) {
	t.Parallel()
	if MatchExpression("a !") != "" { // single rune + punctuation → no usable tokens
		t.Fatal("expected empty match expression for token-less query")
	}
}
