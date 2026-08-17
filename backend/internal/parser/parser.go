package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/symbols"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

const (
	maxSymbolCodeBytes = 16_000
	maxDocCommentBytes = 1_024
)

var identifierPattern = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)

type Engine struct{}

func New() *Engine { return &Engine{} }

func DetectLanguage(path string) (treesitter.Language, string, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return treesitter.LanguageGo, "go", true
	case ".js", ".mjs", ".cjs":
		return treesitter.LanguageJavaScript, "javascript", true
	case ".jsx":
		return treesitter.LanguageTSX, "javascriptreact", true
	case ".ts", ".mts", ".cts":
		return treesitter.LanguageTypeScript, "typescript", true
	case ".tsx":
		return treesitter.LanguageTSX, "typescriptreact", true
	case ".swift":
		return treesitter.LanguageSwift, "swift", true
	case ".py":
		return treesitter.LanguagePython, "python", true
	case ".rs":
		return treesitter.LanguageRust, "rust", true
	default:
		return "", "", false
	}
}

func (e *Engine) Parse(path string, source []byte) ([]domain.Symbol, []domain.Edge, string, error) {
	language, _, ok := DetectLanguage(path)
	if !ok {
		return nil, nil, "", fmt.Errorf("unsupported source file: %s", path)
	}

	tree, err := treesitter.Parse(language, source)
	if err != nil {
		return nil, nil, "", err
	}
	defer tree.Close()
	return e.ExtractTree(path, source, tree.RootNode())
}

// ExtractTree normalizes an already-parsed Tree-sitter root into CodeAtlas
// symbols and edges. The caller must keep the owning tree alive for the entire
// call; parse sessions satisfy that contract through WithTree.
func (e *Engine) ExtractTree(path string, source []byte, root treesitter.Node) ([]domain.Symbol, []domain.Edge, string, error) {
	language, languageName, ok := DetectLanguage(path)
	if !ok {
		return nil, nil, "", fmt.Errorf("unsupported source file: %s", path)
	}
	if root.IsNull() {
		return nil, nil, "", fmt.Errorf("source tree has no root: %s", path)
	}
	fileID := temporarySymbolID(0)
	fileSymbol := domain.Symbol{
		ID:            fileID,
		Path:          path,
		Name:          filepath.Base(path),
		QualifiedName: path,
		Kind:          "file",
		Language:      languageName,
		Range:         nodeRange(root),
		Signature:     path,
		Code:          truncate(string(source), maxSymbolCodeBytes),
	}

	state := &walkState{
		path:         path,
		language:     language,
		languageName: languageName,
		source:       source,
		symbols:      []domain.Symbol{fileSymbol},
		localNames:   map[string][]string{filepath.Base(path): {fileID}},
		nextSymbolID: 1,
	}
	if language == treesitter.LanguagePython {
		state.symbols[0].DocComment = state.pythonDocString(root)
	}
	state.walk(root, &fileSymbol)
	state.resolveLocalEdges()
	state.addSummaries()
	if err := state.resolveStableHandles(); err != nil {
		return nil, nil, "", err
	}

	return state.symbols, state.edges, languageName, nil
}

type walkState struct {
	path         string
	language     treesitter.Language
	languageName string
	source       []byte
	symbols      []domain.Symbol
	edges        []domain.Edge
	localNames   map[string][]string
	nextSymbolID int
}

func temporarySymbolID(index int) string { return fmt.Sprintf("parse:%d", index) }

func (s *walkState) newTemporarySymbolID() string {
	id := temporarySymbolID(s.nextSymbolID)
	s.nextSymbolID++
	return id
}

func (s *walkState) walk(node treesitter.Node, parent *domain.Symbol) {
	if node.IsNull() {
		return
	}
	if s.walkMultiImport(node, parent) {
		return
	}

	current := parent
	if candidate, ok := s.symbolFromNode(node, parent); ok {
		s.symbols = append(s.symbols, candidate)
		s.localNames[candidate.Name] = append(s.localNames[candidate.Name], candidate.ID)
		s.edges = append(s.edges, domain.Edge{
			FromSymbolID: parent.ID,
			ToSymbolID:   candidate.ID,
			ToName:       candidate.QualifiedName,
			Type:         "contains",
			Path:         s.path,
			Line:         candidate.Range.Start.Line,
			Confidence:   1,
		})
		current = &s.symbols[len(s.symbols)-1]
	}

	s.captureRelationship(node, current)

	for i := uint32(0); i < node.NamedChildCount(); i++ {
		s.walk(node.NamedChild(i), current)
	}
}

func (s *walkState) symbolFromNode(node treesitter.Node, parent *domain.Symbol) (domain.Symbol, bool) {
	if s.language == treesitter.LanguageGo && node.Type() == "import_spec" {
		return s.importSpecSymbol(node)
	}
	if s.language == treesitter.LanguageSwift && node.Type() == "import_declaration" {
		return s.swiftImportSymbol(node)
	}
	if s.language == treesitter.LanguageRust && node.Type() == "impl_item" {
		return s.rustImplSymbol(node)
	}
	if s.language == treesitter.LanguageSwift && node.Type() == "property_declaration" && parent != nil {
		switch parent.Kind {
		case domain.KindFunction, domain.KindMethod, domain.KindTest:
			// Local bindings remain semantic-only overlay targets. Persisting them
			// as fields pollutes global name resolution and invents type members.
			return domain.Symbol{}, false
		}
	}
	if s.language == treesitter.LanguagePython && node.Type() == "assignment" && s.pythonHasFunctionAncestor(node) {
		// Function-local bindings are semantic-only overlay targets. Persisting
		// them would turn dynamic local state into repository-wide symbols.
		return domain.Symbol{}, false
	}

	kind, nameNode, ok := s.symbolDescriptor(node)
	if !ok || nameNode.IsNull() {
		return domain.Symbol{}, false
	}

	name := strings.TrimSpace(nameNode.Text(s.source))
	if s.language == treesitter.LanguageSwift && nameNode.Type() == "pattern" {
		if bound := nameNode.ChildByFieldName("bound_identifier"); !bound.IsNull() {
			name = strings.TrimSpace(bound.Text(s.source))
		}
	}
	if s.language == treesitter.LanguagePython && node.Type() == "assignment" && nameNode.Type() != "identifier" {
		// Attribute/subscript/tuple assignment can be monkey-patching, mutation,
		// or destructuring. None establishes one deterministic named symbol.
		return domain.Symbol{}, false
	}
	if name == "" {
		return domain.Symbol{}, false
	}

	qualified := name
	if parent != nil && parent.Kind != "file" {
		qualified = parent.QualifiedName + "." + name
	} else if s.language == treesitter.LanguageGo && node.Type() == "method_declaration" {
		if receiver := receiverType(node.ChildByFieldName("receiver").Text(s.source)); receiver != "" {
			qualified = receiver + "." + name
		}
	}
	if !strings.Contains(qualified, "::") {
		qualified = s.path + "::" + qualified
	}

	code := strings.TrimSpace(node.Text(s.source))
	rangeValue := nodeRange(node)
	if s.language == treesitter.LanguageSwift && (kind == domain.KindFunction) && parent != nil && parent.Kind != domain.KindFile {
		kind = domain.KindMethod
	}
	if s.language == treesitter.LanguageSwift && kind == domain.KindVariable && parent != nil && parent.Kind != domain.KindFile {
		kind = domain.KindField
	}
	if s.language == treesitter.LanguagePython {
		if kind == domain.KindFunction {
			if s.pythonHasPropertyDecorator(node) {
				kind = domain.KindField
			} else if s.pythonIsMethod(node) {
				kind = domain.KindMethod
			}
		}
		if kind == domain.KindVariable && parent != nil && parent.Kind != domain.KindFile {
			kind = domain.KindField
		}
	}
	if s.language == treesitter.LanguageRust && kind == domain.KindFunction && s.rustFunctionHasSelf(node) {
		kind = domain.KindMethod
	}
	if isTestSymbol(s.language, kind, name, s.path) {
		kind = "test"
	}
	if s.language == treesitter.LanguageRust && kind == domain.KindFunction && s.rustHasAttribute(node, "test") {
		kind = domain.KindTest
	}
	return domain.Symbol{
		ID:            s.newTemporarySymbolID(),
		Path:          s.path,
		Name:          name,
		QualifiedName: qualified,
		Kind:          kind,
		Language:      s.languageName,
		Range:         rangeValue,
		Signature:     signature(code),
		Code:          truncate(code, maxSymbolCodeBytes),
		DocComment:    s.docComment(node),
	}, true
}

// docComment resolves only comments structurally adjacent to the declaration.
// It checks the symbol node first (grouped Go type specs), then transparent
// declaration wrappers such as type_declaration/export_statement. This avoids
// attaching a file header or a class comment to its first nested method.
func (s *walkState) docComment(node treesitter.Node) string {
	if s.language == treesitter.LanguagePython {
		return s.pythonDocString(node)
	}
	for candidate := node; !candidate.IsNull(); candidate = docWrapperParent(candidate) {
		comments := s.precedingComments(candidate)
		if len(comments) == 0 {
			continue
		}
		if s.language != treesitter.LanguageGo && s.language != treesitter.LanguageSwift && s.language != treesitter.LanguageRust && !isJSDoc(comments[len(comments)-1]) {
			return ""
		}
		if s.language == treesitter.LanguageSwift && !isSwiftDoc(comments[len(comments)-1]) {
			return ""
		}
		if s.language == treesitter.LanguageRust && !isRustDoc(comments[len(comments)-1]) {
			return ""
		}
		doc := normalizeDocComments(comments)
		if doc == "" || isLicenseHeader(doc) {
			return ""
		}
		if len(doc) > maxDocCommentBytes {
			doc = textutil.TruncateUTF8(doc, maxDocCommentBytes-len("…")) + "…"
		}
		return doc
	}
	return ""
}

func docWrapperParent(node treesitter.Node) treesitter.Node {
	parent := node.Parent()
	if parent.IsNull() {
		return treesitter.Node{}
	}
	switch parent.Type() {
	case "type_declaration", "lexical_declaration", "variable_declaration", "export_statement":
		return parent
	default:
		return treesitter.Node{}
	}
}

func (s *walkState) precedingComments(node treesitter.Node) []string {
	var reversed []string
	current := node
	if s.language == treesitter.LanguageRust {
		for previous := current.PrevNamedSibling(); !previous.IsNull() && previous.Type() == "attribute_item"; previous = current.PrevNamedSibling() {
			current = previous
		}
	}
	for previous := current.PrevNamedSibling(); !previous.IsNull() && s.isCommentNode(previous); previous = current.PrevNamedSibling() {
		if !commentsAreAdjacent(s.source, previous.EndByte(), current.StartByte()) {
			break
		}
		reversed = append(reversed, previous.Text(s.source))
		current = previous
	}
	comments := make([]string, len(reversed))
	for i := range reversed {
		comments[len(reversed)-1-i] = reversed[i]
	}
	return comments
}

func (s *walkState) isCommentNode(node treesitter.Node) bool {
	if node.Type() == "comment" {
		return true
	}
	return s.language == treesitter.LanguageRust && (node.Type() == "line_comment" || node.Type() == "block_comment")
}

func commentsAreAdjacent(source []byte, beforeEnd, afterStart uint32) bool {
	if beforeEnd > afterStart || int(afterStart) > len(source) {
		return false
	}
	gap := strings.ReplaceAll(string(source[beforeEnd:afterStart]), "\r\n", "\n")
	return strings.TrimSpace(gap) == "" && strings.Count(gap, "\n") <= 1
}

func isJSDoc(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "/**")
}

func isSwiftDoc(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "/**")
}

func isRustDoc(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "//!") ||
		strings.HasPrefix(trimmed, "/**") || strings.HasPrefix(trimmed, "/*!")
}

func normalizeDocComments(comments []string) string {
	var lines []string
	for _, raw := range comments {
		value := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(value, "//"):
			value = strings.TrimPrefix(value, "//")
			value = strings.TrimPrefix(value, "/")
			value = strings.TrimPrefix(value, "!")
			lines = append(lines, strings.TrimSpace(value))
		case strings.HasPrefix(value, "/*"):
			value = strings.TrimPrefix(value, "/*")
			value = strings.TrimSuffix(value, "*/")
			value = strings.TrimPrefix(value, "!")
			for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
				line = strings.TrimSpace(line)
				line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
				lines = append(lines, line)
			}
		}
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (s *walkState) swiftImportSymbol(node treesitter.Node) (domain.Symbol, bool) {
	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(node.Text(s.source)), "import"))
	fields := strings.Fields(target)
	if len(fields) == 0 {
		return domain.Symbol{}, false
	}
	target = fields[len(fields)-1]
	name := target
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	if name == "" {
		return domain.Symbol{}, false
	}
	return domain.Symbol{
		ID: s.newTemporarySymbolID(), Path: s.path, Name: name,
		QualifiedName: s.path + "::" + target, Kind: domain.KindImport, Language: s.languageName,
		Range: nodeRange(node), Signature: "import " + target,
		Code: truncate(node.Text(s.source), maxSymbolCodeBytes),
	}, true
}

type importBinding struct {
	name   string
	target string
	node   treesitter.Node
}

func (s *walkState) walkMultiImport(node treesitter.Node, parent *domain.Symbol) bool {
	var bindings []importBinding
	switch {
	case s.language == treesitter.LanguagePython && (node.Type() == "import_statement" || node.Type() == "import_from_statement"):
		bindings = s.pythonImportBindings(node)
	case s.language == treesitter.LanguageRust && node.Type() == "use_declaration":
		argument := node.ChildByFieldName("argument")
		bindings = s.rustUseBindings(argument, "")
	default:
		return false
	}

	raw := strings.TrimSpace(node.Text(s.source))
	prefix := "import:"
	if s.language == treesitter.LanguageRust {
		prefix = "use:"
	}
	for _, binding := range bindings {
		if binding.name == "" || binding.target == "" {
			continue
		}
		rangeValue := nodeRange(node)
		if !binding.node.IsNull() {
			rangeValue = nodeRange(binding.node)
		}
		symbol := domain.Symbol{
			ID: s.newTemporarySymbolID(), Path: s.path, Name: binding.name,
			QualifiedName: s.path + "::" + prefix + binding.target,
			Kind:          domain.KindImport, Language: s.languageName, Range: rangeValue,
			Signature: signature(raw), Code: truncate(raw, maxSymbolCodeBytes),
		}
		s.symbols = append(s.symbols, symbol)
		s.localNames[symbol.Name] = append(s.localNames[symbol.Name], symbol.ID)
		s.edges = append(s.edges,
			domain.Edge{FromSymbolID: parent.ID, ToSymbolID: symbol.ID, ToName: symbol.QualifiedName, Type: "contains", Path: s.path, Line: rangeValue.Start.Line, Confidence: 1},
			domain.Edge{FromSymbolID: currentFileID(s.symbols), ToName: binding.target, Type: "imports", Path: s.path, Line: rangeValue.Start.Line, Confidence: 0.95},
		)
	}
	if s.language == treesitter.LanguagePython && node.Type() == "import_from_statement" {
		module := strings.TrimSpace(node.ChildByFieldName("module_name").Text(s.source))
		module = strings.ReplaceAll(strings.TrimLeft(module, "."), ".", "/")
		if module != "" {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: currentFileID(s.symbols), ToName: module, Type: "imports",
				Path: s.path, Line: int(node.StartPoint().Row) + 1, Confidence: 0.95,
			})
		}
	}
	return true
}

func (s *walkState) pythonImportBindings(node treesitter.Node) []importBinding {
	module := ""
	moduleNode := node.ChildByFieldName("module_name")
	if !moduleNode.IsNull() {
		module = strings.TrimSpace(moduleNode.Text(s.source))
	}
	bindings := make([]importBinding, 0, node.NamedChildCount())
	for i := uint32(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if !moduleNode.IsNull() && child.StartByte() == moduleNode.StartByte() && child.EndByte() == moduleNode.EndByte() {
			continue
		}
		nameNode := child
		alias := ""
		if child.Type() == "aliased_import" {
			nameNode = child.ChildByFieldName("name")
			alias = strings.TrimSpace(child.ChildByFieldName("alias").Text(s.source))
		}
		if nameNode.IsNull() || (nameNode.Type() != "dotted_name" && nameNode.Type() != "relative_import") {
			continue
		}
		imported := strings.TrimSpace(nameNode.Text(s.source))
		if imported == "" {
			continue
		}
		target := imported
		if module != "" {
			target = strings.TrimSuffix(module, ".") + "." + strings.TrimPrefix(imported, ".")
		}
		name := alias
		if name == "" {
			name = imported
			if index := strings.LastIndex(name, "."); index >= 0 {
				name = name[index+1:]
			}
		}
		bindings = append(bindings, importBinding{name: name, target: target, node: child})
	}
	return bindings
}

func (s *walkState) rustUseBindings(node treesitter.Node, prefix string) []importBinding {
	if node.IsNull() {
		return nil
	}
	join := func(left, right string) string {
		left, right = strings.TrimSuffix(strings.TrimSpace(left), "::"), strings.TrimPrefix(strings.TrimSpace(right), "::")
		if left == "" {
			return right
		}
		if right == "" {
			return left
		}
		return left + "::" + right
	}
	switch node.Type() {
	case "scoped_use_list":
		path := strings.TrimSpace(node.ChildByFieldName("path").Text(s.source))
		return s.rustUseBindings(node.ChildByFieldName("list"), join(prefix, path))
	case "use_list":
		var bindings []importBinding
		for i := uint32(0); i < node.NamedChildCount(); i++ {
			bindings = append(bindings, s.rustUseBindings(node.NamedChild(i), prefix)...)
		}
		return bindings
	case "use_as_clause":
		path := strings.TrimSpace(node.ChildByFieldName("path").Text(s.source))
		alias := strings.TrimSpace(node.ChildByFieldName("alias").Text(s.source))
		if alias == "" {
			return nil
		}
		target := join(prefix, path)
		return []importBinding{{name: alias, target: target + " as " + alias, node: node}}
	case "use_wildcard":
		target := strings.TrimSpace(node.Text(s.source))
		target = join(prefix, target)
		name := strings.TrimSuffix(strings.TrimSuffix(target, "*"), "::")
		if index := strings.LastIndex(name, "::"); index >= 0 {
			name = name[index+2:]
		}
		return []importBinding{{name: name, target: target, node: node}}
	default:
		value := strings.TrimSpace(node.Text(s.source))
		if value == "" {
			return nil
		}
		target := join(prefix, value)
		name := value
		if value == "self" && prefix != "" {
			name = prefix
		}
		if index := strings.LastIndex(name, "::"); index >= 0 {
			name = name[index+2:]
		}
		return []importBinding{{name: name, target: target, node: node}}
	}
}

func (s *walkState) rustImplSymbol(node treesitter.Node) (domain.Symbol, bool) {
	typeName := strings.TrimSpace(node.ChildByFieldName("type").Text(s.source))
	if typeName == "" {
		return domain.Symbol{}, false
	}
	name := typeName
	if traitName := strings.TrimSpace(node.ChildByFieldName("trait").Text(s.source)); traitName != "" {
		name = traitName + " for " + typeName
	}
	return domain.Symbol{
		ID: s.newTemporarySymbolID(), Path: s.path, Name: name,
		QualifiedName: s.path + "::impl:" + name, Kind: domain.KindType, Language: s.languageName,
		Range: nodeRange(node), Signature: signature(node.Text(s.source)),
		Code: truncate(strings.TrimSpace(node.Text(s.source)), maxSymbolCodeBytes), DocComment: s.docComment(node),
	}, true
}

func isLicenseHeader(doc string) bool {
	lower := strings.ToLower(doc)
	return strings.Contains(lower, "spdx-license-identifier") ||
		strings.Contains(lower, "copyright") ||
		strings.Contains(lower, "licensed under") ||
		strings.Contains(lower, "all rights reserved")
}

func (s *walkState) importSpecSymbol(node treesitter.Node) (domain.Symbol, bool) {
	pathNode := node.ChildByFieldName("path")
	if pathNode.IsNull() {
		return domain.Symbol{}, false
	}
	path := trimQuotes(pathNode.Text(s.source))
	if path == "" {
		return domain.Symbol{}, false
	}

	name := ""
	nameNode := node.ChildByFieldName("name")
	if !nameNode.IsNull() {
		alias := strings.TrimSpace(nameNode.Text(s.source))
		if alias != "." && alias != "_" {
			name = alias
		}
	}
	if name == "" {
		name = filepath.Base(path)
	}
	if name == "" {
		return domain.Symbol{}, false
	}

	qualified := path
	if !strings.Contains(qualified, "::") {
		qualified = s.path + "::" + qualified
	}
	rangeValue := nodeRange(node)
	return domain.Symbol{
		ID:            s.newTemporarySymbolID(),
		Path:          s.path,
		Name:          name,
		QualifiedName: qualified,
		Kind:          "import",
		Language:      s.languageName,
		Range:         rangeValue,
		Signature:     "import " + path,
		Code:          truncate(node.Text(s.source), maxSymbolCodeBytes),
	}, true
}

func (s *walkState) symbolDescriptor(node treesitter.Node) (kind string, name treesitter.Node, ok bool) {
	typeName := node.Type()
	switch s.language {
	case treesitter.LanguageGo:
		switch typeName {
		case "function_declaration":
			return "function", node.ChildByFieldName("name"), true
		case "method_declaration":
			return "method", node.ChildByFieldName("name"), true
		case "type_spec":
			return "type", node.ChildByFieldName("name"), true
		}
	case treesitter.LanguageSwift:
		switch typeName {
		case "class_declaration":
			declarationKind := strings.TrimSpace(node.ChildByFieldName("declaration_kind").Text(s.source))
			kind := domain.KindType
			switch declarationKind {
			case "class", "actor":
				kind = domain.KindClass
			case "enum":
				kind = domain.KindEnum
			}
			return kind, node.ChildByFieldName("name"), true
		case "protocol_declaration":
			return domain.KindInterface, node.ChildByFieldName("name"), true
		case "function_declaration", "protocol_function_declaration":
			return domain.KindFunction, node.ChildByFieldName("name"), true
		case "init_declaration":
			return domain.KindMethod, node.ChildByFieldName("name"), true
		case "property_declaration", "protocol_property_declaration":
			return domain.KindVariable, node.ChildByFieldName("name"), true
		case "enum_entry":
			return domain.KindField, node.ChildByFieldName("name"), true
		case "typealias_declaration", "associatedtype_declaration":
			return domain.KindType, node.ChildByFieldName("name"), true
		}
	case treesitter.LanguagePython:
		switch typeName {
		case "class_definition":
			return domain.KindClass, node.ChildByFieldName("name"), true
		case "function_definition":
			return domain.KindFunction, node.ChildByFieldName("name"), true
		case "assignment":
			return domain.KindVariable, node.ChildByFieldName("left"), true
		}
	case treesitter.LanguageRust:
		switch typeName {
		case "mod_item":
			return domain.KindPackage, node.ChildByFieldName("name"), true
		case "struct_item", "union_item":
			return domain.KindType, node.ChildByFieldName("name"), true
		case "enum_item":
			return domain.KindEnum, node.ChildByFieldName("name"), true
		case "trait_item":
			return domain.KindInterface, node.ChildByFieldName("name"), true
		case "function_item", "function_signature_item":
			return domain.KindFunction, node.ChildByFieldName("name"), true
		case "const_item", "static_item":
			return domain.KindVariable, node.ChildByFieldName("name"), true
		case "field_declaration", "enum_variant":
			return domain.KindField, node.ChildByFieldName("name"), true
		case "type_item", "associated_type":
			return domain.KindType, node.ChildByFieldName("name"), true
		case "macro_definition":
			// Macro expansion is intentionally not evaluated. A definition is
			// still a stable callable declaration for retrieval and navigation.
			return domain.KindFunction, node.ChildByFieldName("name"), true
		}
	default:
		switch typeName {
		case "function_declaration", "generator_function_declaration":
			return "function", node.ChildByFieldName("name"), true
		case "class_declaration", "abstract_class_declaration":
			return "class", node.ChildByFieldName("name"), true
		case "method_definition", "abstract_method_signature", "method_signature":
			return "method", node.ChildByFieldName("name"), true
		case "interface_declaration":
			return "interface", node.ChildByFieldName("name"), true
		case "type_alias_declaration":
			return "type", node.ChildByFieldName("name"), true
		case "enum_declaration":
			return "enum", node.ChildByFieldName("name"), true
		case "variable_declarator":
			value := node.ChildByFieldName("value")
			switch value.Type() {
			case "arrow_function", "function_expression", "generator_function", "class":
				return "function", node.ChildByFieldName("name"), true
			}
		}
	}
	return "", treesitter.Node{}, false
}

func (s *walkState) captureRelationship(node treesitter.Node, current *domain.Symbol) {
	if current == nil {
		return
	}

	switch node.Type() {
	case "call_expression", "call":
		if node.Type() == "call" && s.language != treesitter.LanguagePython {
			return
		}
		functionNode := node.ChildByFieldName("function")
		if functionNode.IsNull() {
			functionNode = node.ChildByFieldName("callee")
		}
		if functionNode.IsNull() && s.language == treesitter.LanguageSwift && node.NamedChildCount() > 0 {
			functionNode = node.NamedChild(0)
		}
		rawTarget := strings.TrimSpace(functionNode.Text(s.source))
		if s.language == treesitter.LanguageRust {
			// Field calls may dispatch through traits at runtime. Keep only direct
			// or statically scoped call syntax; rust-analyzer may enrich the rest.
			switch functionNode.Type() {
			case "identifier", "scoped_identifier":
			case "generic_function":
				functionNode = functionNode.ChildByFieldName("function")
				if functionNode.IsNull() {
					return
				}
				rawTarget = strings.TrimSpace(functionNode.Text(s.source))
			default:
				return
			}
		}
		// Chained Go calls such as s.clock().UTC() do not name a package-level
		// callable. Keeping only the last token used to create a false external
		// node named UTC in Codemaps.
		if s.language == treesitter.LanguageGo && strings.ContainsAny(rawTarget, "()\n") {
			return
		}
		target := normalizeCallTarget(rawTarget)
		if s.language == treesitter.LanguagePython {
			// Qualified/subscripted call targets are runtime dispatch in Python.
			// Keep them as an explicit omission instead of resolving their final
			// token to an unrelated repository symbol.
			if !isPythonIdentifier(rawTarget) || isPythonDynamicBuiltin(rawTarget) {
				return
			}
			target = rawTarget
		}
		if target == "" {
			return
		}
		if s.language == treesitter.LanguageGo && isGoBuiltin(target) {
			return
		}
		s.edges = append(s.edges, domain.Edge{
			FromSymbolID: current.ID,
			ToName:       target,
			Type:         "calls",
			Path:         s.path,
			Line:         int(node.StartPoint().Row) + 1,
			Confidence:   0.85,
		})
	case "import_declaration":
		if s.language != treesitter.LanguageSwift {
			return
		}
		target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(node.Text(s.source)), "import"))
		fields := strings.Fields(target)
		if len(fields) > 0 {
			target = fields[len(fields)-1]
		}
		if target != "" {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: currentFileID(s.symbols), ToName: target, Type: "imports",
				Path: s.path, Line: int(node.StartPoint().Row) + 1, Confidence: 0.95,
			})
		}
	case "import_spec":
		pathNode := node.ChildByFieldName("path")
		if pathNode.IsNull() {
			pathNode = node
		}
		target := trimQuotes(pathNode.Text(s.source))
		if target != "" {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: currentFileID(s.symbols),
				ToName:       target,
				Type:         "imports",
				Path:         s.path,
				Line:         int(node.StartPoint().Row) + 1,
				Confidence:   0.95,
			})
		}
	case "import_statement":
		sourceNode := node.ChildByFieldName("source")
		target := trimQuotes(sourceNode.Text(s.source))
		if target != "" {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: currentFileID(s.symbols),
				ToName:       target,
				Type:         "imports",
				Path:         s.path,
				Line:         int(node.StartPoint().Row) + 1,
				Confidence:   0.95,
			})
		}
	case "impl_item":
		if s.language != treesitter.LanguageRust {
			return
		}
		traitName := rustTypeBaseName(strings.TrimSpace(node.ChildByFieldName("trait").Text(s.source)))
		if traitName != "" {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: current.ID, ToName: traitName, Type: "implements",
				Path: s.path, Line: int(node.StartPoint().Row) + 1, Confidence: 0.95,
			})
		}
	case "class_definition":
		if s.language != treesitter.LanguagePython {
			return
		}
		for _, base := range pythonClassBases(node.ChildByFieldName("superclasses").Text(s.source)) {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: current.ID, ToName: base, Type: "inherits",
				Path: s.path, Line: int(node.StartPoint().Row) + 1, Confidence: 0.9,
			})
		}
	case "export_statement":
		// `export {name} from './module'` and `export * from './module'`
		// are module dependencies just like imports for graph traversal. The
		// source code/range preserves that this is a re-export without adding a
		// graph edge kind the persisted contract cannot validate.
		sourceNode := node.ChildByFieldName("source")
		target := trimQuotes(sourceNode.Text(s.source))
		if target != "" {
			s.edges = append(s.edges, domain.Edge{
				FromSymbolID: currentFileID(s.symbols),
				ToName:       target,
				Type:         "imports",
				Path:         s.path,
				Line:         int(node.StartPoint().Row) + 1,
				Confidence:   0.95,
			})
		}
	}
}

func (s *walkState) resolveLocalEdges() {
	for i := range s.edges {
		if s.edges[i].ToSymbolID != "" {
			continue
		}
		ids := s.localNames[s.edges[i].ToName]
		if len(ids) == 0 {
			name := finalIdentifier(s.edges[i].ToName)
			ids = s.localNames[name]
		}
		if len(ids) == 1 {
			s.edges[i].ToSymbolID = ids[0]
			s.edges[i].Confidence = 1
		}
	}
}

func (s *walkState) addSummaries() {
	callsBySymbol := make(map[string][]string)
	childrenBySymbol := make(map[string]int)
	for _, edge := range s.edges {
		switch edge.Type {
		case "calls":
			callsBySymbol[edge.FromSymbolID] = append(callsBySymbol[edge.FromSymbolID], edge.ToName)
		case "contains":
			childrenBySymbol[edge.FromSymbolID]++
		}
	}

	for i := range s.symbols {
		symbol := &s.symbols[i]
		calls := uniqueSorted(callsBySymbol[symbol.ID])
		parts := []string{fmt.Sprintf("%s %s em %s", symbol.Kind, symbol.Name, symbol.Path)}
		if symbol.Kind == "file" {
			parts = []string{fmt.Sprintf("File %s with %d indexed symbols", symbol.Path, childrenBySymbol[symbol.ID])}
		}
		if len(calls) > 0 {
			if len(calls) > 5 {
				calls = calls[:5]
			}
			parts = append(parts, "calls "+strings.Join(calls, ", "))
		}
		symbol.Summary = strings.Join(parts, "; ") + "."
	}
}

func nodeRange(node treesitter.Node) domain.Range {
	start := node.StartPoint()
	end := node.EndPoint()
	return domain.Range{
		Start: domain.Position{Line: int(start.Row) + 1, Column: int(start.Column) + 1},
		End:   domain.Position{Line: int(end.Row) + 1, Column: int(end.Column) + 1},
	}
}

func (s *walkState) resolveStableHandles() error {
	resolved, handleByTemporary, err := symbols.ResolveHierarchy(s.symbols, s.edges, "")
	if err != nil {
		return fmt.Errorf("resolve symbol identities for %s: %w", s.path, err)
	}
	for index := range resolved {
		s.symbols[index] = resolved[index].ToSymbol()
	}
	for index := range s.edges {
		if handle, exists := handleByTemporary[s.edges[index].FromSymbolID]; exists {
			s.edges[index].FromSymbolID = handle
		}
		if handle, exists := handleByTemporary[s.edges[index].ToSymbolID]; exists {
			s.edges[index].ToSymbolID = handle
		}
	}
	return nil
}

func signature(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	lines := strings.FieldsFunc(code, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) == 0 {
		return truncate(code, 280)
	}
	first := strings.TrimSpace(lines[0])
	if index := strings.Index(first, "{"); index >= 0 {
		first = strings.TrimSpace(first[:index])
	}
	return truncate(first, 280)
}

func truncate(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	const suffix = "\n…"
	if max <= len(suffix) {
		return textutil.TruncateUTF8("…", max)
	}
	return textutil.TruncateUTF8(value, max-len(suffix)) + suffix
}

func normalizeCallTarget(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "new ")
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "(){}[]\n") {
		return finalIdentifier(value)
	}
	if strings.HasPrefix(value, "(") {
		return ""
	}
	return strings.TrimSpace(value)
}

var goBuiltins = map[string]struct{}{
	"append": {}, "cap": {}, "clear": {}, "close": {}, "complex": {},
	"copy": {}, "delete": {}, "imag": {}, "len": {}, "make": {},
	"max": {}, "min": {}, "new": {}, "panic": {}, "print": {},
	"println": {}, "real": {}, "recover": {},
}

func isGoBuiltin(value string) bool {
	_, ok := goBuiltins[finalIdentifier(value)]
	return ok
}

func finalIdentifier(value string) string {
	matches := identifierPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func receiverType(value string) string {
	matches := identifierPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func trimQuotes(value string) string {
	return strings.Trim(strings.TrimSpace(value), "`\"'")
}

func currentFileID(symbols []domain.Symbol) string {
	if len(symbols) == 0 {
		return ""
	}
	return symbols[0].ID
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func isTestName(name, path string) bool {
	lowerPath := strings.ToLower(path)
	return strings.HasPrefix(name, "Test") || strings.HasSuffix(lowerPath, "_test.go") ||
		strings.Contains(lowerPath, ".test.") || strings.Contains(lowerPath, ".spec.")
}

func isTestSymbol(language treesitter.Language, kind, name, path string) bool {
	if language == treesitter.LanguageRust {
		return false // Rust test attributes are inspected structurally below.
	}
	if language == treesitter.LanguagePython {
		lowerPath := strings.ToLower(filepath.ToSlash(path))
		base := strings.ToLower(filepath.Base(lowerPath))
		isTestFile := strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
			strings.Contains(lowerPath, "/tests/") || strings.HasPrefix(lowerPath, "tests/")
		if !isTestFile {
			return false
		}
		return (kind == domain.KindFunction || kind == domain.KindMethod) && strings.HasPrefix(strings.ToLower(name), "test_") ||
			kind == domain.KindClass && (strings.HasPrefix(name, "Test") || strings.HasSuffix(name, "Tests") || strings.HasSuffix(name, "TestCase"))
	}
	if language != treesitter.LanguageSwift {
		return kind == domain.KindFunction && isTestName(name, path)
	}
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	if !strings.Contains(lowerPath, "/tests/") && !strings.HasPrefix(lowerPath, "tests/") {
		return false
	}
	return (kind == domain.KindFunction || kind == domain.KindMethod) && strings.HasPrefix(name, "test") ||
		(kind == domain.KindClass && strings.HasSuffix(name, "Tests"))
}

func (s *walkState) rustFunctionHasSelf(node treesitter.Node) bool {
	if s.language != treesitter.LanguageRust || (node.Type() != "function_item" && node.Type() != "function_signature_item") {
		return false
	}
	parameters := node.ChildByFieldName("parameters")
	if parameters.IsNull() {
		return false
	}
	for i := uint32(0); i < parameters.NamedChildCount(); i++ {
		if parameters.NamedChild(i).Type() == "self_parameter" {
			return true
		}
	}
	return false
}

func (s *walkState) rustHasAttribute(node treesitter.Node, name string) bool {
	if s.language != treesitter.LanguageRust {
		return false
	}
	for previous := node.PrevNamedSibling(); !previous.IsNull() && previous.Type() == "attribute_item"; previous = previous.PrevNamedSibling() {
		value := strings.Join(strings.Fields(previous.Text(s.source)), "")
		if value == "#["+name+"]" {
			return true
		}
	}
	return false
}

func rustTypeBaseName(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.Index(value, "<"); index >= 0 {
		value = value[:index]
	}
	parts := strings.Split(value, "::")
	return strings.TrimSpace(parts[len(parts)-1])
}

var pythonStringPrefixes = "rRuUbB"

func (s *walkState) pythonDocString(node treesitter.Node) string {
	body := node
	if node.Type() == "class_definition" || node.Type() == "function_definition" {
		body = node.ChildByFieldName("body")
	}
	if body.IsNull() || body.NamedChildCount() == 0 {
		return ""
	}
	statement := body.NamedChild(0)
	if statement.Type() != "expression_statement" || statement.NamedChildCount() == 0 {
		return ""
	}
	literal := statement.NamedChild(0)
	if literal.Type() != "string" {
		return ""
	}
	value := strings.TrimSpace(literal.Text(s.source))
	for len(value) > 0 && strings.ContainsRune(pythonStringPrefixes, rune(value[0])) {
		value = value[1:]
	}
	var delimiter string
	switch {
	case strings.HasPrefix(value, `"""`):
		delimiter = `"""`
	case strings.HasPrefix(value, `'''`):
		delimiter = `'''`
	case strings.HasPrefix(value, `"`):
		delimiter = `"`
	case strings.HasPrefix(value, `'`):
		delimiter = `'`
	default:
		return ""
	}
	if !strings.HasSuffix(value, delimiter) || len(value) < 2*len(delimiter) {
		return ""
	}
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, delimiter), delimiter))
	if len(value) > maxDocCommentBytes {
		value = textutil.TruncateUTF8(value, maxDocCommentBytes-len("…")) + "…"
	}
	return value
}

func (s *walkState) pythonHasFunctionAncestor(node treesitter.Node) bool {
	for parent := node.Parent(); !parent.IsNull(); parent = parent.Parent() {
		switch parent.Type() {
		case "function_definition":
			return true
		case "module":
			return false
		}
	}
	return false
}

func (s *walkState) pythonIsMethod(node treesitter.Node) bool {
	for parent := node.Parent(); !parent.IsNull(); parent = parent.Parent() {
		switch parent.Type() {
		case "function_definition":
			return false
		case "class_definition":
			return true
		case "module":
			return false
		}
	}
	return false
}

func (s *walkState) pythonHasPropertyDecorator(node treesitter.Node) bool {
	for sibling := node.PrevNamedSibling(); !sibling.IsNull() && sibling.Type() == "decorator"; sibling = sibling.PrevNamedSibling() {
		if s.pythonDecoratorIsProperty(sibling) {
			return true
		}
	}
	parent := node.Parent()
	for !parent.IsNull() && parent.Type() != "decorated_definition" {
		if parent.Type() == "function_definition" || parent.Type() == "class_definition" || parent.Type() == "module" {
			return false
		}
		parent = parent.Parent()
	}
	if parent.IsNull() {
		return false
	}
	for i := uint32(0); i < parent.NamedChildCount(); i++ {
		child := parent.NamedChild(i)
		if child.Type() != "decorator" {
			continue
		}
		if s.pythonDecoratorIsProperty(child) {
			return true
		}
	}
	return false
}

func (s *walkState) pythonDecoratorIsProperty(node treesitter.Node) bool {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(node.Text(s.source)), "@"))
	if index := strings.IndexByte(name, '('); index >= 0 {
		name = name[:index]
	}
	return name == "property" || strings.HasSuffix(name, ".property") ||
		name == "cached_property" || strings.HasSuffix(name, ".cached_property")
}

func pythonClassBases(raw string) []string {
	raw = strings.Trim(strings.TrimSpace(raw), "()")
	if raw == "" {
		return nil
	}
	var bases []string
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if isPythonQualifiedIdentifier(candidate) {
			bases = append(bases, candidate)
		}
	}
	return bases
}

func isPythonIdentifier(value string) bool {
	return isPythonQualifiedIdentifier(value) && !strings.Contains(value, ".")
}

func isPythonQualifiedIdentifier(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for index, r := range part {
			if index == 0 {
				if r != '_' && !unicode.IsLetter(r) {
					return false
				}
			} else if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}

var pythonDynamicBuiltins = map[string]struct{}{
	"__import__": {}, "eval": {}, "exec": {}, "getattr": {}, "setattr": {}, "delattr": {},
}

func isPythonDynamicBuiltin(name string) bool {
	_, dynamic := pythonDynamicBuiltins[name]
	return dynamic
}
