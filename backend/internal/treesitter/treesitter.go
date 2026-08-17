package treesitter

/*
#cgo CFLAGS: -std=c11 -O2 -D_DEFAULT_SOURCE -I${SRCDIR}/vendor/tree-sitter/lib/include -I${SRCDIR}/vendor/tree-sitter/lib/src -I${SRCDIR}/grammars/include
#include <stdlib.h>
#include "tree_sitter/api.h"

const TSLanguage *tree_sitter_go(void);
const TSLanguage *tree_sitter_javascript(void);
const TSLanguage *tree_sitter_typescript(void);
const TSLanguage *tree_sitter_tsx(void);
const TSLanguage *tree_sitter_swift(void);
const TSLanguage *tree_sitter_python(void);
const TSLanguage *tree_sitter_rust(void);
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"unsafe"
)

type Language string

const (
	LanguageGo         Language = "go"
	LanguageJavaScript Language = "javascript"
	LanguageTypeScript Language = "typescript"
	LanguageTSX        Language = "tsx"
	LanguageSwift      Language = "swift"
	LanguagePython     Language = "python"
	LanguageRust       Language = "rust"
)

// ErrClosed is returned when a Tree or Parser is used after Close.
var ErrClosed = errors.New("tree-sitter: resource is closed")

// noCopy makes `go vet` flag accidental copies of a Parser/Tree by value, which
// would alias a cgo pointer and risk a double free.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Point is a position in the source. Column is a byte offset in the UTF-8 source
// (as in the Tree-sitter C API), NOT a rune count and NOT a UTF-16/LSP column.
type Point struct {
	Row    uint32
	Column uint32
}

// InputEdit describes a single contiguous edit to the source, in UTF-8 bytes and
// byte-column Points. The caller computes it; this layer validates but never
// infers points.
type InputEdit struct {
	StartByte   uint32
	OldEndByte  uint32
	NewEndByte  uint32
	StartPoint  Point
	OldEndPoint Point
	NewEndPoint Point
}

// validate rejects an obviously malformed edit before crossing into C.
func (e InputEdit) validate() error {
	if e.StartByte > e.OldEndByte {
		return fmt.Errorf("tree-sitter: edit StartByte %d > OldEndByte %d", e.StartByte, e.OldEndByte)
	}
	if e.StartByte > e.NewEndByte {
		return fmt.Errorf("tree-sitter: edit StartByte %d > NewEndByte %d", e.StartByte, e.NewEndByte)
	}
	if pointGreater(e.StartPoint, e.OldEndPoint) {
		return fmt.Errorf("tree-sitter: edit StartPoint after OldEndPoint")
	}
	if pointGreater(e.StartPoint, e.NewEndPoint) {
		return fmt.Errorf("tree-sitter: edit StartPoint after NewEndPoint")
	}
	return nil
}

func pointGreater(a, b Point) bool {
	if a.Row != b.Row {
		return a.Row > b.Row
	}
	return a.Column > b.Column
}

// Range is a contiguous span of the source in bytes and byte-column Points.
type Range struct {
	StartByte  uint32
	EndByte    uint32
	StartPoint Point
	EndPoint   Point
}

// Node is a handle into a parsed Tree. It keeps a reference to its owner Tree so
// methods can fail closed once the Tree is freed, rather than dereferencing freed
// C memory.
type Node struct {
	raw  C.TSNode
	tree *Tree
}

func (n Node) ownerClosed() bool {
	return n.tree != nil && n.tree.ptr == nil
}

func (n Node) IsNull() bool {
	if n.ownerClosed() {
		return true
	}
	result := bool(C.ts_node_is_null(n.raw))
	runtime.KeepAlive(n.tree)
	return result
}

func (n Node) Type() string {
	if n.IsNull() {
		return ""
	}
	value := C.GoString(C.ts_node_type(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

func (n Node) StartByte() uint32 {
	if n.IsNull() {
		return 0
	}
	value := uint32(C.ts_node_start_byte(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

func (n Node) EndByte() uint32 {
	if n.IsNull() {
		return 0
	}
	value := uint32(C.ts_node_end_byte(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

// HasError reports whether the node or any descendant is an error node.
func (n Node) HasError() bool {
	if n.ownerClosed() {
		return false
	}
	value := bool(C.ts_node_has_error(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

// IsMissing reports whether the node was inserted by error recovery.
func (n Node) IsMissing() bool {
	if n.ownerClosed() {
		return false
	}
	value := bool(C.ts_node_is_missing(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

func (n Node) StartPoint() Point {
	if n.IsNull() {
		return Point{}
	}
	point := C.ts_node_start_point(n.raw)
	runtime.KeepAlive(n.tree)
	return Point{Row: uint32(point.row), Column: uint32(point.column)}
}

func (n Node) EndPoint() Point {
	if n.IsNull() {
		return Point{}
	}
	point := C.ts_node_end_point(n.raw)
	runtime.KeepAlive(n.tree)
	return Point{Row: uint32(point.row), Column: uint32(point.column)}
}

func (n Node) NamedChildCount() uint32 {
	if n.IsNull() {
		return 0
	}
	value := uint32(C.ts_node_named_child_count(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

func (n Node) NamedChild(index uint32) Node {
	if n.IsNull() {
		return Node{}
	}
	raw := C.ts_node_named_child(n.raw, C.uint32_t(index))
	runtime.KeepAlive(n.tree)
	return Node{raw: raw, tree: n.tree}
}

func (n Node) ChildCount() uint32 {
	if n.IsNull() {
		return 0
	}
	value := uint32(C.ts_node_child_count(n.raw))
	runtime.KeepAlive(n.tree)
	return value
}

func (n Node) Child(index uint32) Node {
	if n.IsNull() {
		return Node{}
	}
	raw := C.ts_node_child(n.raw, C.uint32_t(index))
	runtime.KeepAlive(n.tree)
	return Node{raw: raw, tree: n.tree}
}

// Parent and PrevNamedSibling expose only structural navigation needed by the
// parser. Returned nodes retain the same Tree owner and fail closed after Close.
func (n Node) Parent() Node {
	if n.IsNull() {
		return Node{}
	}
	raw := C.ts_node_parent(n.raw)
	runtime.KeepAlive(n.tree)
	return Node{raw: raw, tree: n.tree}
}

func (n Node) PrevNamedSibling() Node {
	if n.IsNull() {
		return Node{}
	}
	raw := C.ts_node_prev_named_sibling(n.raw)
	runtime.KeepAlive(n.tree)
	return Node{raw: raw, tree: n.tree}
}

func (n Node) ChildByFieldName(name string) Node {
	if n.IsNull() {
		return Node{}
	}
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	raw := C.ts_node_child_by_field_name(n.raw, cName, C.uint32_t(len(name)))
	runtime.KeepAlive(n.tree)
	return Node{raw: raw, tree: n.tree}
}

func (n Node) NamedDescendantForPointRange(start, end Point) Node {
	if n.IsNull() {
		return Node{}
	}
	raw := C.ts_node_named_descendant_for_point_range(
		n.raw,
		C.TSPoint{row: C.uint32_t(start.Row), column: C.uint32_t(start.Column)},
		C.TSPoint{row: C.uint32_t(end.Row), column: C.uint32_t(end.Column)},
	)
	runtime.KeepAlive(n.tree)
	return Node{raw: raw, tree: n.tree}
}

func (n Node) Text(source []byte) string {
	start, end := n.StartByte(), n.EndByte()
	if end < start || int(end) > len(source) {
		return ""
	}
	return string(source[start:end])
}

// Parser is a reusable Tree-sitter parser bound to one language. It is not safe
// for concurrent use; its own mutex serializes Parse/Close so a stray concurrent
// call fails closed rather than corrupting the C parser.
type Parser struct {
	_        noCopy
	mu       sync.Mutex
	ptr      *C.TSParser
	language Language
}

// NewParser allocates a parser for language. Close it when done.
func NewParser(language Language) (*Parser, error) {
	lang := languagePointer(language)
	if lang == nil {
		return nil, fmt.Errorf("tree-sitter: unsupported language %q", language)
	}
	ptr := C.ts_parser_new()
	if ptr == nil {
		return nil, errors.New("tree-sitter: could not allocate parser")
	}
	if !bool(C.ts_parser_set_language(ptr, lang)) {
		C.ts_parser_delete(ptr)
		return nil, fmt.Errorf("tree-sitter: language ABI mismatch for %q", language)
	}
	return &Parser{ptr: ptr, language: language}, nil
}

// Parse parses source, reusing oldTree for incremental reparse when non-nil.
// oldTree must have been Edit-ed to reflect the change and must be the same
// language. The returned Tree owns its own C memory.
func (p *Parser) Parse(source []byte, oldTree *Tree) (*Tree, error) {
	if len(source) > math.MaxUint32 {
		return nil, fmt.Errorf("tree-sitter: source length %d exceeds uint32", len(source))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return nil, ErrClosed
	}

	var oldPtr *C.TSTree
	if oldTree != nil {
		if oldTree.ptr == nil {
			return nil, fmt.Errorf("tree-sitter: %w (old tree)", ErrClosed)
		}
		if oldTree.language != p.language {
			return nil, fmt.Errorf("tree-sitter: incremental parse language %q != tree language %q", p.language, oldTree.language)
		}
		oldPtr = oldTree.ptr
	}

	var input *C.char
	if len(source) > 0 {
		input = (*C.char)(C.CBytes(source))
		defer C.free(unsafe.Pointer(input))
	}
	tree := C.ts_parser_parse_string(p.ptr, oldPtr, input, C.uint32_t(len(source)))
	runtime.KeepAlive(oldTree)
	if tree == nil {
		return nil, errors.New("tree-sitter: parse returned nil tree")
	}
	return newTree(tree, p.language, uint32(len(source))), nil
}

// Close releases the parser. It is idempotent and safe to call after a partial
// failure.
func (p *Parser) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return nil
	}
	C.ts_parser_delete(p.ptr)
	p.ptr = nil
	return nil
}

// Tree is a parsed syntax tree. It tracks its language and current byte size so
// edits can be validated, and is not safe to copy by value.
type Tree struct {
	_        noCopy
	ptr      *C.TSTree
	language Language
	size     uint32
}

func newTree(ptr *C.TSTree, language Language, size uint32) *Tree {
	tree := &Tree{ptr: ptr, language: language, size: size}
	// A finalizer is a backstop for a forgotten Close, not the primary mechanism;
	// callers are expected to Close explicitly.
	runtime.SetFinalizer(tree, (*Tree).Close)
	return tree
}

// Language reports the language the tree was parsed with; it distinguishes
// TypeScript from TSX even though they share a runtime.
func (t *Tree) Language() Language {
	if t == nil {
		return ""
	}
	return t.language
}

// RootNode returns the tree's root, owned by this tree.
func (t *Tree) RootNode() Node {
	if t == nil || t.ptr == nil {
		return Node{}
	}
	raw := C.ts_tree_root_node(t.ptr)
	runtime.KeepAlive(t)
	return Node{raw: raw, tree: t}
}

// Edit applies an InputEdit to the tree so the next incremental Parse can reuse
// it. The edit is validated (and bounded by the tree's known size) before
// crossing into C.
func (t *Tree) Edit(edit InputEdit) error {
	if t == nil || t.ptr == nil {
		return ErrClosed
	}
	if err := edit.validate(); err != nil {
		return err
	}
	if edit.OldEndByte > t.size {
		return fmt.Errorf("tree-sitter: edit OldEndByte %d beyond tree size %d", edit.OldEndByte, t.size)
	}
	cEdit := C.TSInputEdit{
		start_byte:    C.uint32_t(edit.StartByte),
		old_end_byte:  C.uint32_t(edit.OldEndByte),
		new_end_byte:  C.uint32_t(edit.NewEndByte),
		start_point:   C.TSPoint{row: C.uint32_t(edit.StartPoint.Row), column: C.uint32_t(edit.StartPoint.Column)},
		old_end_point: C.TSPoint{row: C.uint32_t(edit.OldEndPoint.Row), column: C.uint32_t(edit.OldEndPoint.Column)},
		new_end_point: C.TSPoint{row: C.uint32_t(edit.NewEndPoint.Row), column: C.uint32_t(edit.NewEndPoint.Column)},
	}
	C.ts_tree_edit(t.ptr, &cEdit)
	runtime.KeepAlive(t)
	// Update the known size: size - removed + inserted.
	t.size = t.size - edit.OldEndByte + edit.NewEndByte
	return nil
}

// Close releases the tree. It is idempotent and clears the finalizer.
func (t *Tree) Close() {
	if t == nil || t.ptr == nil {
		return
	}
	C.ts_tree_delete(t.ptr)
	t.ptr = nil
	runtime.SetFinalizer(t, nil)
}

// ChangedRanges returns the source ranges that differ between an edited old tree
// and the reparsed new tree. Both trees must be open; the old tree must have been
// Edit-ed and remain valid until this returns.
func ChangedRanges(editedOldTree, newTree *Tree) ([]Range, error) {
	if editedOldTree == nil || editedOldTree.ptr == nil || newTree == nil || newTree.ptr == nil {
		return nil, ErrClosed
	}
	var count C.uint32_t
	ptr := C.ts_tree_get_changed_ranges(editedOldTree.ptr, newTree.ptr, &count)
	runtime.KeepAlive(editedOldTree)
	runtime.KeepAlive(newTree)
	if ptr == nil {
		return nil, nil
	}
	// ts_tree_get_changed_ranges allocates with the library allocator (malloc by
	// default); free it with C.free.
	defer C.free(unsafe.Pointer(ptr))
	if count == 0 {
		return nil, nil
	}
	cRanges := unsafe.Slice(ptr, int(count))
	ranges := make([]Range, int(count))
	for i := range cRanges {
		ranges[i] = Range{
			StartByte:  uint32(cRanges[i].start_byte),
			EndByte:    uint32(cRanges[i].end_byte),
			StartPoint: Point{Row: uint32(cRanges[i].start_point.row), Column: uint32(cRanges[i].start_point.column)},
			EndPoint:   Point{Row: uint32(cRanges[i].end_point.row), Column: uint32(cRanges[i].end_point.column)},
		}
	}
	return ranges, nil
}

// Parse is a convenience full parse: it allocates a single-use Parser, parses
// source from scratch, and is implemented on top of Parser.
func Parse(language Language, source []byte) (*Tree, error) {
	parser, err := NewParser(language)
	if err != nil {
		return nil, err
	}
	defer parser.Close()
	return parser.Parse(source, nil)
}

func languagePointer(language Language) *C.TSLanguage {
	switch language {
	case LanguageGo:
		return C.tree_sitter_go()
	case LanguageJavaScript:
		return C.tree_sitter_javascript()
	case LanguageTypeScript:
		return C.tree_sitter_typescript()
	case LanguageTSX:
		return C.tree_sitter_tsx()
	case LanguageSwift:
		return C.tree_sitter_swift()
	case LanguagePython:
		return C.tree_sitter_python()
	case LanguageRust:
		return C.tree_sitter_rust()
	default:
		return nil
	}
}
