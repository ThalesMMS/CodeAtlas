package parser

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

func TestParseTruncatedSymbolCodePreservesUTF8(t *testing.T) {
	t.Parallel()
	prefix := "package p\n\n// "
	source := prefix + strings.Repeat("x", maxSymbolCodeBytes-len(prefix)-1) + "érest\nfunc F() {}\n"
	symbols, _, _, err := New().Parse("p.go", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(symbols) == 0 || symbols[0].Kind != "file" {
		t.Fatalf("missing file symbol: %#v", symbols)
	}
	if !utf8.ValidString(symbols[0].Code) {
		t.Fatalf("persisted file symbol code is invalid UTF-8")
	}
}

func TestExtractTreeMatchesFullParse(t *testing.T) {
	t.Parallel()
	const source = "package checkout\n\nfunc Submit() { Notify() }\nfunc Notify() {}\n"
	engine := New()
	wantSymbols, wantEdges, wantLanguage, err := engine.Parse("checkout/service.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := treesitter.Parse(treesitter.LanguageGo, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	gotSymbols, gotEdges, gotLanguage, err := engine.ExtractTree("checkout/service.go", []byte(source), tree.RootNode())
	if err != nil {
		t.Fatal(err)
	}
	if gotLanguage != wantLanguage || !reflect.DeepEqual(gotSymbols, wantSymbols) || !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Fatalf("ExtractTree differs from Parse\nlanguage: got %q want %q\nsymbols: got %#v want %#v\nedges: got %#v want %#v", gotLanguage, wantLanguage, gotSymbols, wantSymbols, gotEdges, wantEdges)
	}
}

func TestParseEmitsStableIdentityAcrossLineShift(t *testing.T) {
	t.Parallel()
	before := []byte("package sample\n\nimport \"fmt\"\n\nfunc A() { B(); fmt.Println() }\nfunc B() {}\n")
	after := []byte("package sample\n\n\n\nimport \"fmt\"\n\nfunc A() { B(); fmt.Println() }\nfunc B() {}\n")
	beforeSymbols, beforeEdges, _, err := New().Parse("sample/sample.go", before)
	if err != nil {
		t.Fatal(err)
	}
	afterSymbols, afterEdges, _, err := New().Parse("sample/sample.go", after)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"sample.go", "A", "B"} {
		beforeSymbol := symbolNamed(t, beforeSymbols, name)
		afterSymbol := symbolNamed(t, afterSymbols, name)
		if !strings.HasPrefix(beforeSymbol.ID, "sym:v1:") {
			t.Fatalf("%s ID = %q, want sym:v1", name, beforeSymbol.ID)
		}
		if beforeSymbol.ID != afterSymbol.ID {
			t.Fatalf("%s identity changed after line shift: %q vs %q", name, beforeSymbol.ID, afterSymbol.ID)
		}
		if beforeSymbol.OccurrenceID == afterSymbol.OccurrenceID {
			t.Fatalf("%s occurrence did not change after line shift", name)
		}
	}
	beforeImport := symbolNamed(t, beforeSymbols, "fmt")
	afterImport := symbolNamed(t, afterSymbols, "fmt")
	if !strings.HasPrefix(beforeImport.ID, "occ:anon:v2:") || beforeImport.OccurrenceID != beforeImport.ID {
		t.Fatalf("import handle = %#v, want occurrence-only v2", beforeImport)
	}
	if beforeImport.ID == afterImport.ID {
		t.Fatal("line-shifted import kept the same physical occurrence ID")
	}

	beforeCall := edgeFromTo(t, beforeEdges, "calls", symbolNamed(t, beforeSymbols, "A").ID, symbolNamed(t, beforeSymbols, "B").ID)
	afterCall := edgeFromTo(t, afterEdges, "calls", symbolNamed(t, afterSymbols, "A").ID, symbolNamed(t, afterSymbols, "B").ID)
	if beforeCall.FromSymbolID != afterCall.FromSymbolID || beforeCall.ToSymbolID != afterCall.ToSymbolID {
		t.Fatalf("resolved edge handles changed: %#v vs %#v", beforeCall, afterCall)
	}
}

func TestParsePreservesDuplicateOccurrencesForOneIdentity(t *testing.T) {
	t.Parallel()
	source := []byte("interface Merge { first(): void }\ninterface Merge { second(): void }\n")
	symbols, _, _, err := New().Parse("merge.ts", source)
	if err != nil {
		t.Fatal(err)
	}
	var declarations []domain.Symbol
	for _, symbol := range symbols {
		if symbol.Name == "Merge" {
			declarations = append(declarations, symbol)
		}
	}
	if len(declarations) != 2 {
		t.Fatalf("Merge declarations = %d, want 2", len(declarations))
	}
	if declarations[0].ID != declarations[1].ID {
		t.Fatalf("duplicate identity IDs differ: %q vs %q", declarations[0].ID, declarations[1].ID)
	}
	if declarations[0].OccurrenceID == declarations[1].OccurrenceID {
		t.Fatal("duplicate declarations collapsed to one occurrence")
	}
}

func symbolNamed(t *testing.T, symbols []domain.Symbol, name string) domain.Symbol {
	t.Helper()
	for _, symbol := range symbols {
		if symbol.Name == name {
			return symbol
		}
	}
	t.Fatalf("symbol %q not found in %#v", name, symbols)
	return domain.Symbol{}
}

func edgeFromTo(t *testing.T, edges []domain.Edge, kind, from, to string) domain.Edge {
	t.Helper()
	for _, edge := range edges {
		if edge.Type == kind && edge.FromSymbolID == from && edge.ToSymbolID == to {
			return edge
		}
	}
	t.Fatalf("edge %s %s -> %s not found in %#v", kind, from, to, edges)
	return domain.Edge{}
}

func TestParseGoSymbolsAndCalls(t *testing.T) {
	t.Parallel()
	const source = `package checkout

type Service struct{}

func NewService() *Service { return &Service{} }

func (s *Service) Submit() error {
	return persist()
}

func persist() error { return nil }
`

	symbols, edges, language, err := New().Parse("checkout/service.go", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if language != "go" {
		t.Fatalf("language = %q, want go", language)
	}
	assertSymbol(t, symbols, "Service", "type")
	assertSymbol(t, symbols, "NewService", "function")
	submit := assertSymbol(t, symbols, "Submit", "method")
	if submit.QualifiedName != "checkout/service.go::Service.Submit" {
		t.Fatalf("Submit qualified name = %q", submit.QualifiedName)
	}
	persist := assertSymbol(t, symbols, "persist", "function")

	foundResolvedCall := false
	for _, edge := range edges {
		if edge.Type == "calls" && edge.FromSymbolID == submit.ID && edge.ToSymbolID == persist.ID {
			foundResolvedCall = true
			break
		}
	}
	if !foundResolvedCall {
		t.Fatalf("expected resolved Submit -> persist call, edges = %#v", edges)
	}
}

func TestParseSwiftSymbolsImportsCallsAndTests(t *testing.T) {
	t.Parallel()
	const source = `import Foundation

/// Persists orders.
protocol Repository {
    func save(_ order: Order) async throws
}

struct Order {
    let id: String
    init(id: String) { self.id = id }
    func display() -> String { render(id) }
}

func render(_ value: String) -> String { value }
`
	symbols, edges, language, err := New().Parse("Sources/Commerce/Order.swift", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if language != "swift" {
		t.Fatalf("language = %q, want swift", language)
	}
	assertSymbol(t, symbols, "Foundation", domain.KindImport)
	repository := assertSymbol(t, symbols, "Repository", domain.KindInterface)
	if repository.DocComment != "Persists orders." {
		t.Fatalf("Repository doc = %q", repository.DocComment)
	}
	assertSymbol(t, symbols, "Order", domain.KindType)
	assertSymbol(t, symbols, "id", domain.KindField)
	assertSymbol(t, symbols, "init", domain.KindMethod)
	display := assertSymbol(t, symbols, "display", domain.KindMethod)
	render := assertSymbol(t, symbols, "render", domain.KindFunction)
	edgeFromTo(t, edges, "calls", display.ID, render.ID)
	if !hasEdge(edges, "imports", "Foundation") {
		t.Fatalf("missing Swift import edge: %#v", edges)
	}

	testSymbols, _, _, err := New().Parse("Tests/CommerceTests/OrderServiceTests.swift", []byte(`import XCTest
final class OrderServiceTests: XCTestCase {
  func testPersists() async throws {
    let repository = MemoryRepository()
    XCTAssertNotNil(repository)
  }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	assertSymbol(t, testSymbols, "OrderServiceTests", domain.KindTest)
	assertSymbol(t, testSymbols, "testPersists", domain.KindTest)
	for _, symbol := range testSymbols {
		if symbol.Name == "repository" {
			t.Fatalf("local Swift binding was persisted as a structural symbol: %#v", symbol)
		}
	}
}

func TestParseSwiftOverloadsHaveStableDistinctIdentities(t *testing.T) {
	t.Parallel()
	const source = `extension Order {
  func displayName() -> String { "Order" }
  func displayName(prefix: String) -> String { prefix }
}
`
	first, _, _, err := New().Parse("Sources/Order+Display.swift", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := New().Parse("Sources/Order+Display.swift", []byte("\n"+source))
	if err != nil {
		t.Fatal(err)
	}
	var firstIDs, secondIDs []string
	for _, symbol := range first {
		if symbol.Name == "displayName" {
			if symbol.Kind != domain.KindMethod || !strings.Contains(symbol.QualifiedName, "Order.displayName") {
				t.Fatalf("Swift extension member = %#v, want Order.displayName method", symbol)
			}
			firstIDs = append(firstIDs, symbol.ID)
		}
	}
	for _, symbol := range second {
		if symbol.Name == "displayName" {
			secondIDs = append(secondIDs, symbol.ID)
		}
	}
	if len(firstIDs) != 2 || len(secondIDs) != 2 || firstIDs[0] == firstIDs[1] {
		t.Fatalf("Swift overload identities = %v / %v", firstIDs, secondIDs)
	}
	if !reflect.DeepEqual(firstIDs, secondIDs) {
		t.Fatalf("Swift overload identities changed after a line shift: %v vs %v", firstIDs, secondIDs)
	}
}

func TestParsePythonSymbolsImportsInheritanceCallsDocsAndTests(t *testing.T) {
	t.Parallel()
	const source = `"""Commerce repository primitives.

These declarations are parsed without importing this module.
"""
from .models import Order
import asyncio as aio
import importlib

DEFAULT_TIMEOUT: float = 2.0

class BaseRepository:
    async def save(self, order: Order) -> None:
        raise NotImplementedError

class MemoryRepository(BaseRepository):
    """Stores orders in memory."""

    orders: dict[str, Order] = {}

    @property
    def count(self) -> int:
        return len(self.orders)

    async def save(self, order: Order) -> None:
        local_value = order
        audit(order)
        self.persist(order)
        importlib.import_module("dynamic_plugin")

def audit(order: Order) -> None:
    pass

def 𐐀calculate(order: Order) -> None:
    audit(order)

MemoryRepository.runtime_patch = audit
`
	symbols, edges, language, err := New().Parse("commerce/repository.py", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if language != "python" {
		t.Fatalf("language = %q, want python", language)
	}
	file := assertSymbol(t, symbols, "repository.py", domain.KindFile)
	if !strings.Contains(file.DocComment, "without importing this module") {
		t.Fatalf("module docstring = %q", file.DocComment)
	}
	assertSymbol(t, symbols, "Order", domain.KindImport)
	assertSymbol(t, symbols, "aio", domain.KindImport)
	assertSymbol(t, symbols, "DEFAULT_TIMEOUT", domain.KindVariable)
	base := assertSymbol(t, symbols, "BaseRepository", domain.KindClass)
	memory := assertSymbol(t, symbols, "MemoryRepository", domain.KindClass)
	if memory.DocComment != "Stores orders in memory." {
		t.Fatalf("MemoryRepository docstring = %q", memory.DocComment)
	}
	assertSymbol(t, symbols, "orders", domain.KindField)
	assertSymbol(t, symbols, "count", domain.KindField)
	var save domain.Symbol
	for _, symbol := range symbols {
		if symbol.Name == "save" && strings.Contains(symbol.QualifiedName, "MemoryRepository.save") {
			save = symbol
		}
	}
	if save.ID == "" {
		t.Fatalf("MemoryRepository.save not found in %#v", symbols)
	}
	audit := assertSymbol(t, symbols, "audit", domain.KindFunction)
	unicodeFunction := assertSymbol(t, symbols, "𐐀calculate", domain.KindFunction)
	edgeFromTo(t, edges, "inherits", memory.ID, base.ID)
	edgeFromTo(t, edges, "calls", save.ID, audit.ID)
	edgeFromTo(t, edges, "calls", unicodeFunction.ID, audit.ID)
	if !hasEdge(edges, "imports", "models") || !hasEdge(edges, "imports", "asyncio") {
		t.Fatalf("missing Python import edges: %#v", edges)
	}
	for _, symbol := range symbols {
		if symbol.Name == "local_value" || symbol.Name == "runtime_patch" {
			t.Fatalf("dynamic/local Python binding was persisted: %#v", symbol)
		}
	}
	for _, edge := range edges {
		if edge.Type == "calls" && (edge.ToName == "persist" || edge.ToName == "import_module") {
			t.Fatalf("dynamic Python dispatch became a deterministic call edge: %#v", edge)
		}
	}

	testSymbols, _, _, err := New().Parse("tests/test_service.py", []byte(`import unittest
import pytest

@pytest.mark.asyncio
async def test_persists_order():
    pass

class OrderTests(unittest.TestCase):
    def test_finds_order(self):
        pass
`))
	if err != nil {
		t.Fatal(err)
	}
	assertSymbol(t, testSymbols, "test_persists_order", domain.KindTest)
	assertSymbol(t, testSymbols, "OrderTests", domain.KindTest)
	assertSymbol(t, testSymbols, "test_finds_order", domain.KindTest)
}

func TestParsePythonStableIdentitiesSurviveLineShift(t *testing.T) {
	t.Parallel()
	const source = `class Service:
    async def submit(self, value: str) -> str:
        return value
`
	first, _, _, err := New().Parse("commerce/service.py", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := New().Parse("commerce/service.py", []byte("\n"+source))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Service", "submit"} {
		var firstID, secondID string
		for _, symbol := range first {
			if symbol.Name == name {
				firstID = symbol.ID
			}
		}
		for _, symbol := range second {
			if symbol.Name == name {
				secondID = symbol.ID
			}
		}
		if firstID == "" || firstID != secondID {
			t.Fatalf("Python %s identity changed after a line shift: %q vs %q", name, firstID, secondID)
		}
	}
}

func TestParsePythonEmitsEveryImportBinding(t *testing.T) {
	t.Parallel()
	const source = `import os, sys as system
from commerce.models import Order, Customer as Buyer
`
	symbols, edges, _, err := New().Parse("commerce/imports.py", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"os", "system", "Order", "Buyer"} {
		assertSymbol(t, symbols, name, domain.KindImport)
	}
	for _, target := range []string{"os", "sys", "commerce.models.Order", "commerce.models.Customer"} {
		if !hasEdge(edges, "imports", target) {
			t.Fatalf("missing Python import edge %q: %#v", target, edges)
		}
	}
}

func TestParseRustSymbolsUsesTraitsImplsCallsMacrosAndTests(t *testing.T) {
	t.Parallel()
	const source = `use crate::models::Order;
use crate::repository::MemoryRepository as Repo;

pub const DEFAULT_CAPACITY: usize = 16;
static INSTANCE_COUNT: usize = 0;

/// Stores a value without executing repository code.
pub trait Repository<T> {
    fn save(&mut self, value: T);
    fn create(value: T) -> Self;
}

pub struct MemoryRepository<T> {
    values: Vec<T>,
}

pub enum RepositoryError {
    Missing,
}

impl<T> Repository<T> for MemoryRepository<T> {
    fn save(&mut self, value: T) {
        audit_call();
        self.values.push(value);
    }

    fn create(value: T) -> Self {
        MemoryRepository { values: vec![value] }
    }
}

macro_rules! audit {
    ($value:expr) => { println!("{}", $value) };
}

fn audit_call() {}
async fn dispatch<T, R: Repository<T>>(repo: &mut R, value: T) {
    repo.save(value);
    audit!("saved");
}

fn r#type() {}
fn 𐐀calculate() { audit_call(); }

#[cfg(test)]
mod tests {
    #[test]
    fn saves_order() {
        let _repo = MemoryRepository::create(1);
    }
}
`
	symbols, edges, language, err := New().Parse("src/repository.rs", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if language != "rust" {
		t.Fatalf("language = %q, want rust", language)
	}
	assertSymbol(t, symbols, "Order", domain.KindImport)
	assertSymbol(t, symbols, "Repo", domain.KindImport)
	assertSymbol(t, symbols, "DEFAULT_CAPACITY", domain.KindVariable)
	assertSymbol(t, symbols, "INSTANCE_COUNT", domain.KindVariable)
	repository := assertSymbol(t, symbols, "Repository", domain.KindInterface)
	if !strings.Contains(repository.DocComment, "without executing repository code") {
		t.Fatalf("Repository doc = %q", repository.DocComment)
	}
	assertSymbol(t, symbols, "MemoryRepository", domain.KindType)
	assertSymbol(t, symbols, "values", domain.KindField)
	assertSymbol(t, symbols, "RepositoryError", domain.KindEnum)
	assertSymbol(t, symbols, "Missing", domain.KindField)
	impl := assertSymbol(t, symbols, "Repository<T> for MemoryRepository<T>", domain.KindType)
	var save domain.Symbol
	for _, symbol := range symbols {
		if symbol.Name == "save" && symbol.Kind == domain.KindMethod {
			save = symbol
		}
	}
	if save.ID == "" {
		t.Fatalf("Rust method save not found in %#v", symbols)
	}
	assertSymbol(t, symbols, "create", domain.KindFunction)
	assertSymbol(t, symbols, "audit", domain.KindFunction)
	auditCall := assertSymbol(t, symbols, "audit_call", domain.KindFunction)
	assertSymbol(t, symbols, "dispatch", domain.KindFunction)
	assertSymbol(t, symbols, "r#type", domain.KindFunction)
	unicodeFunction := assertSymbol(t, symbols, "𐐀calculate", domain.KindFunction)
	assertSymbol(t, symbols, "tests", domain.KindPackage)
	assertSymbol(t, symbols, "saves_order", domain.KindTest)
	edgeFromTo(t, edges, "implements", impl.ID, repository.ID)
	edgeFromTo(t, edges, "calls", save.ID, auditCall.ID)
	edgeFromTo(t, edges, "calls", unicodeFunction.ID, auditCall.ID)
	if !hasEdge(edges, "imports", "crate::models::Order") || !hasEdge(edges, "imports", "crate::repository::MemoryRepository as Repo") {
		t.Fatalf("missing Rust use edges: %#v", edges)
	}
	for _, edge := range edges {
		if edge.Type == "calls" && (edge.ToName == "save" || edge.ToName == "audit" || edge.ToName == "println") {
			t.Fatalf("dynamic dispatch or macro expansion became a deterministic call edge: %#v", edge)
		}
	}
}

func TestParseRustStableIdentitiesSurviveLineShift(t *testing.T) {
	t.Parallel()
	const source = `struct Service<T> { value: T }
impl<T> Service<T> {
    fn submit(&self) {}
}
`
	first, _, _, err := New().Parse("src/service.rs", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := New().Parse("src/service.rs", []byte("\n"+source))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Service", "Service<T>", "submit"} {
		var firstID, secondID string
		for _, symbol := range first {
			if symbol.Name == name {
				firstID = symbol.ID
			}
		}
		for _, symbol := range second {
			if symbol.Name == name {
				secondID = symbol.ID
			}
		}
		if firstID == "" || firstID != secondID {
			t.Fatalf("Rust %s identity changed after a line shift: %q vs %q", name, firstID, secondID)
		}
	}
}

func TestParseRustEmitsGroupedUseBindingsAndNormalizesInnerDocs(t *testing.T) {
	t.Parallel()
	const source = `use crate::{models::{Order, Customer as Buyer}, repository::Repo};

//! Inner line documentation.
/*! Inner block documentation. */
pub struct Catalog;
`
	symbols, edges, _, err := New().Parse("src/catalog.rs", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Order", "Buyer", "Repo"} {
		assertSymbol(t, symbols, name, domain.KindImport)
	}
	for _, target := range []string{
		"crate::models::Order",
		"crate::models::Customer as Buyer",
		"crate::repository::Repo",
	} {
		if !hasEdge(edges, "imports", target) {
			t.Fatalf("missing Rust grouped-use edge %q: %#v", target, edges)
		}
	}
	catalog := assertSymbol(t, symbols, "Catalog", domain.KindType)
	if strings.Contains(catalog.DocComment, "!") || !strings.Contains(catalog.DocComment, "Inner line documentation") {
		t.Fatalf("Catalog inner docs = %q", catalog.DocComment)
	}
}

func hasEdge(edges []domain.Edge, kind, target string) bool {
	for _, edge := range edges {
		if edge.Type == kind && edge.ToName == target {
			return true
		}
	}
	return false
}

func TestParseGoDropsBuiltinsAndUnresolvedLocalReceiverCalls(t *testing.T) {
	t.Parallel()
	const source = `package order

import "errors"

type Repository interface { Save() error }
type Service struct {
	repository Repository
	clock func() int
}

func (s *Service) Submit() error {
	values := make([]int, 0, 1)
	values = append(values, s.clock().UTC())
	if err := s.repository.Save(); err != nil {
		return errors.New(err.Error())
	}
	return nil
}
`

	symbols, edges, _, err := New().Parse("internal/order/service.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	submit := assertSymbol(t, symbols, "Submit", "method")
	for _, edge := range edges {
		if edge.FromSymbolID != submit.ID || edge.Type != "calls" {
			continue
		}
		for _, forbidden := range []string{"make", "append", "UTC"} {
			if edge.ToName == forbidden {
				t.Fatalf("false call edge %q was retained: %#v", forbidden, edges)
			}
		}
	}
	assertCallTarget(t, edges, submit.ID, "errors.New", false)
}

func TestParseGoImportSymbols(t *testing.T) {
	t.Parallel()
	const source = `package main

import (
	"log"
	"example.com/tinycommerce/internal/order"
)

func main() {
	order.NewHandler(nil)
}
`

	symbols, edges, language, err := New().Parse("cmd/api/main.go", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if language != "go" {
		t.Fatalf("language = %q, want go", language)
	}

	importSymbol := assertSymbol(t, symbols, "order", "import")
	if importSymbol.QualifiedName != "cmd/api/main.go::example.com/tinycommerce/internal/order" {
		t.Fatalf("import qualified name = %q", importSymbol.QualifiedName)
	}

	foundImportEdge := false
	for _, edge := range edges {
		if edge.Type == "imports" && edge.ToName == "example.com/tinycommerce/internal/order" {
			foundImportEdge = true
			break
		}
	}
	if !foundImportEdge {
		t.Fatalf("expected imports edge, edges = %#v", edges)
	}
}

func TestParseTypeScriptNestedSymbols(t *testing.T) {
	t.Parallel()
	const source = `export class CheckoutService {
  complete(id: string): string {
    return persist(id)
  }
}

export const submitOrder = async (id: string) => persist(id)
function persist(id: string): string { return id }
`

	symbols, _, language, err := New().Parse("web/checkout.ts", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if language != "typescript" {
		t.Fatalf("language = %q, want typescript", language)
	}
	class := assertSymbol(t, symbols, "CheckoutService", "class")
	method := assertSymbol(t, symbols, "complete", "method")
	if method.QualifiedName != class.QualifiedName+".complete" {
		t.Fatalf("method qualified name = %q, class = %q", method.QualifiedName, class.QualifiedName)
	}
	assertSymbol(t, symbols, "submitOrder", "function")
	assertSymbol(t, symbols, "persist", "function")
}

func TestParseMixedJavaScriptTypeScriptImportsReexportsAndJSXDeclarations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path       string
		language   string
		source     string
		symbolName string
		imports    []string
	}{
		{path: "web/index.mjs", language: "javascript", source: "import { View } from './view.js'\nexport { model } from './model.mts'\nexport const App = () => View()\n", symbolName: "App", imports: []string{"./view.js", "./model.mts"}},
		{path: "web/view.jsx", language: "javascriptreact", source: "export const View = () => <main>ok</main>\n", symbolName: "View"},
		{path: "web/model.mts", language: "typescript", source: "export interface Model { id: string }\nexport const model: Model = { id: '1' }\n", symbolName: "Model"},
		{path: "web/view.tsx", language: "typescriptreact", source: "export const TypedView = (value: string) => <span>{value}</span>\n", symbolName: "TypedView"},
		{path: "web/common.cts", language: "typescript", source: "export function common(): number { return 1 }\n", symbolName: "common"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			symbols, edges, language, err := New().Parse(test.path, []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if language != test.language {
				t.Fatalf("language = %q, want %q", language, test.language)
			}
			found := false
			for _, symbol := range symbols {
				if symbol.Name == test.symbolName {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("symbol %q missing from %+v", test.symbolName, symbols)
			}
			for _, expected := range test.imports {
				matched := false
				for _, edge := range edges {
					if edge.Type == "imports" && edge.ToName == expected {
						matched = true
						break
					}
				}
				if !matched {
					t.Fatalf("module dependency %q missing from %+v", expected, edges)
				}
			}
		})
	}
}

func TestParseGoDocComments(t *testing.T) {
	t.Parallel()
	const source = `// Copyright 2026 Example Corp.
// Licensed under the Example License.

package checkout

// Service coordinates checkout.
// It keeps persistence behind a repository.
type Service struct{}

func undocumented() {}
`
	symbols, _, _, err := New().Parse("checkout/service.go", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	service := assertSymbol(t, symbols, "Service", "type")
	if service.DocComment != "Service coordinates checkout.\nIt keeps persistence behind a repository." {
		t.Fatalf("Service DocComment = %q", service.DocComment)
	}
	if undocumented := assertSymbol(t, symbols, "undocumented", "function"); undocumented.DocComment != "" {
		t.Fatalf("undocumented symbol inherited comment %q", undocumented.DocComment)
	}
}

func TestParseDoesNotAttachLicenseHeaderToFirstJavaScriptSymbol(t *testing.T) {
	t.Parallel()
	const source = `/** Copyright 2026 Example Corp. Licensed under the Example License. */
export function bootstrap() {}
`
	symbols, _, _, err := New().Parse("src/app.js", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if bootstrap := assertSymbol(t, symbols, "bootstrap", "function"); bootstrap.DocComment != "" {
		t.Fatalf("license header attached as documentation: %q", bootstrap.DocComment)
	}
}

func TestParseTypeScriptJSDoc(t *testing.T) {
	t.Parallel()
	const source = `/**
 * CheckoutController coordinates the browser checkout flow.
 * @remarks Calls the typed API client.
 */
export class CheckoutController {}
`
	symbols, _, _, err := New().Parse("web/checkout.ts", []byte(source))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	class := assertSymbol(t, symbols, "CheckoutController", "class")
	want := "CheckoutController coordinates the browser checkout flow.\n@remarks Calls the typed API client."
	if class.DocComment != want {
		t.Fatalf("CheckoutController DocComment = %q, want %q", class.DocComment, want)
	}
}

func TestTinycommerceOrderDocCommentIsExtracted(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "examples", "tinycommerce", "internal", "order", "model.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tinycommerce model: %v", err)
	}
	symbols, _, _, err := New().Parse("internal/order/model.go", source)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	order := assertSymbol(t, symbols, "Order", "type")
	if order.DocComment != "Order is the aggregate accepted by the checkout flow." {
		t.Fatalf("Order DocComment = %q", order.DocComment)
	}
}

func TestTruncateReservesSuffixWithinByteLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 1, 2, 3, 4, 8} {
		got := truncate("ááááá", limit)
		if len(got) > limit {
			t.Fatalf("truncate(..., %d) returned %d bytes: %q", limit, len(got), got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(..., %d) returned invalid UTF-8: %q", limit, got)
		}
	}
}

func assertSymbol(t *testing.T, symbols []domain.Symbol, name, kind string) domain.Symbol {
	t.Helper()
	for _, symbol := range symbols {
		if symbol.Name == name && symbol.Kind == kind {
			return symbol
		}
	}
	t.Fatalf("symbol %s (%s) not found in %#v", name, kind, symbols)
	return domain.Symbol{}
}

func assertCallTarget(t *testing.T, edges []domain.Edge, sourceID, targetName string, wantResolved bool) {
	t.Helper()
	for _, edge := range edges {
		if edge.Type != "calls" || edge.FromSymbolID != sourceID || edge.ToName != targetName {
			continue
		}
		if wantResolved && edge.ToSymbolID == "" {
			t.Fatalf("call %s -> %s is unresolved: %#v", sourceID, targetName, edge)
		}
		return
	}
	t.Fatalf("call %s -> %s not found in %#v", sourceID, targetName, edges)
}
