package contextpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/parser"
)

func TestEvidenceFromSymbolPutsDocumentationBeforeDefinitionAndSummary(t *testing.T) {
	t.Parallel()
	symbol := domain.Symbol{
		ID: "sym:v1:order", OccurrenceID: "occ:v1:order", Path: "internal/order/model.go",
		Name: "Order", QualifiedName: "internal/order/model.go::Order",
		Code:       "type Order struct { ID string }",
		DocComment: "Order is the aggregate accepted by the checkout flow.",
		Summary:    "type Order em internal/order/model.go.",
	}
	evidence := evidenceFromSymbol(symbol, KindASTObservation)
	if !strings.HasPrefix(evidence.Content, "Documentation:\nOrder is the aggregate accepted by the checkout flow.") {
		t.Fatalf("documentation is not first in evidence: %q", evidence.Content)
	}
	if !strings.Contains(evidence.Content, "Definition:\ntype Order struct") ||
		!strings.Contains(evidence.Content, "Structural summary:\ntype Order em") {
		t.Fatalf("definition or secondary structural summary missing: %q", evidence.Content)
	}
	if evidence.Title != "internal/order/model.go::Order — Order is the aggregate accepted by the checkout flow." {
		t.Fatalf("evidence title does not prefer doc comment: %q", evidence.Title)
	}
}

func TestEvidenceFromUndocumentedSymbolKeepsDefinition(t *testing.T) {
	t.Parallel()
	evidence := evidenceFromSymbol(domain.Symbol{Name: "F", Code: "func F() {}"}, KindASTObservation)
	if evidence.Title != "F" || evidence.Content != "Definition:\nfunc F() {}" {
		t.Fatalf("undocumented evidence = %#v", evidence)
	}
}

func TestTinycommerceOrderEvidenceContainsDocComment(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "examples", "tinycommerce", "internal", "order", "model.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	symbols, _, _, err := parser.New().Parse("internal/order/model.go", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range symbols {
		if symbol.Name != "Order" {
			continue
		}
		evidence := evidenceFromSymbol(symbol, KindASTObservation)
		if !strings.Contains(evidence.Content, "Order is the aggregate accepted by the checkout flow.") {
			t.Fatalf("Order evidence = %q", evidence.Content)
		}
		return
	}
	t.Fatal("Order symbol not parsed")
}
