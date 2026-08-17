package domain

import "strings"

// SymbolKind enumerates the symbol kinds the parser emits. They are codified here
// so identity/occurrence policy can branch on them without scattered string
// literals.
const (
	KindFile      = "file"
	KindImport    = "import"
	KindPackage   = "package"
	KindFunction  = "function"
	KindMethod    = "method"
	KindType      = "type"
	KindClass     = "class"
	KindInterface = "interface"
	KindEnum      = "enum"
	KindTest      = "test"
	KindVariable  = "variable"
	KindField     = "field"
	// KindSymbol is the conservative fallback for a semantic provider target
	// whose bounded hover text does not expose a language-specific declaration
	// form. It is transport/display metadata and is never persisted as an indexed
	// identity by the parser.
	KindSymbol = "symbol"
)

// IdentityPolicy records, per special symbol type, whether the symbol carries a
// stable logical identity and the reasoning. These documented decisions are
// implemented by the versioned symbols module and parser remapping.
//
//	File symbols .............. identity from the path (a file is a stable logical
//	                            entity; it has no line to shift).
//	Imports ................... occurrence-only — an import is a reference/statement,
//	                            not a symbol definition, in every supported language.
//	Anonymous / arrow fns ..... occurrence-only unless assigned to a named binding
//	                            (then the binding name provides the identity).
//	Same name, different parent identities are distinct because QualifiedName
//	                            embeds the parent chain.
//	Go pointer vs value receiver same identity — the receiver form is not part of
//	                            the logical method identity.
//	Tests ..................... regular identity with kind "test".
//	TypeScript overloads ...... one identity per signature discriminator.
//	Duplicate decl / iface merge same identity, multiple occurrences.
//	Unnamed / no stable name .. occurrence-only.

// occurrenceOnlyKinds are kinds that never carry a logical identity.
var occurrenceOnlyKinds = map[string]struct{}{
	KindImport: {},
}

// IsOccurrenceOnly reports whether a symbol of the given kind/name lacks a stable
// logical identity and must therefore be modeled as occurrence-only. The rule is
// intentionally conservative: a symbol with no usable name, or whose kind is
// inherently a reference (import), is occurrence-only.
func IsOccurrenceOnly(kind, name, qualifiedName string) bool {
	if _, only := occurrenceOnlyKinds[kind]; only {
		return true
	}
	if strings.TrimSpace(qualifiedName) == "" && strings.TrimSpace(name) == "" {
		return true
	}
	if isAnonymousName(name) && strings.TrimSpace(qualifiedName) == "" {
		return true
	}
	return false
}

// isAnonymousName recognizes the synthetic names tree-sitter assigns to
// unnamed/arrow functions.
func isAnonymousName(name string) bool {
	trimmed := strings.TrimSpace(name)
	switch trimmed {
	case "", "(anonymous)", "anonymous", "<anonymous>", "(arrow)":
		return true
	}
	return strings.HasPrefix(trimmed, "(") // tree-sitter placeholders like "(arrow function)"
}
