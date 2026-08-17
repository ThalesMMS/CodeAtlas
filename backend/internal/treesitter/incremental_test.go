package treesitter

import (
	"strings"
	"testing"
)

// pointAt returns the byte-column Point at a byte offset in src.
func pointAt(src []byte, offset uint32) Point {
	row, col := uint32(0), uint32(0)
	for i := uint32(0); int(i) < len(src) && i < offset; i++ {
		if src[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return Point{Row: row, Column: col}
}

// sexp builds an S-expression of named structure for comparing two trees.
func sexp(node Node) string {
	if node.IsNull() {
		return "<null>"
	}
	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(node.Type())
	count := node.NamedChildCount()
	for i := uint32(0); i < count; i++ {
		b.WriteByte(' ')
		b.WriteString(sexp(node.NamedChild(i)))
	}
	b.WriteByte(')')
	return b.String()
}

// appendEdit returns the InputEdit for appending suffix to base.
func appendEdit(base, full []byte) InputEdit {
	return InputEdit{
		StartByte:   uint32(len(base)),
		OldEndByte:  uint32(len(base)),
		NewEndByte:  uint32(len(full)),
		StartPoint:  pointAt(base, uint32(len(base))),
		OldEndPoint: pointAt(base, uint32(len(base))),
		NewEndPoint: pointAt(full, uint32(len(full))),
	}
}

func TestIncrementalParseMatchesFreshParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		language Language
		base     string
		suffix   string
	}{
		{"Go", LanguageGo, "package s\nfunc a() {}\n", "func b() {}\n"},
		{"JavaScript", LanguageJavaScript, "function a() { return 1 }\n", "function b() { return 2 }\n"},
		{"TypeScript", LanguageTypeScript, "const a = (id: string): string => id\n", "const b = (n: number): number => n\n"},
		{"TSX", LanguageTSX, "const A = () => <main>x</main>\n", "const B = () => <div>y</div>\n"},
		{"Swift", LanguageSwift, "struct A { let id: Int }\n", "struct B { let name: String }\n"},
		{"Python", LanguagePython, "def a():\n    return 1\n", "def b():\n    return 2\n"},
		{"Rust", LanguageRust, "fn a() -> i32 { 1 }\n", "fn b() -> i32 { 2 }\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := []byte(tc.base)
			full := []byte(tc.base + tc.suffix)

			parser, err := NewParser(tc.language)
			if err != nil {
				t.Fatalf("NewParser: %v", err)
			}
			defer parser.Close()

			oldTree, err := parser.Parse(base, nil)
			if err != nil {
				t.Fatalf("parse base: %v", err)
			}
			defer oldTree.Close()
			if err := oldTree.Edit(appendEdit(base, full)); err != nil {
				t.Fatalf("Edit: %v", err)
			}
			incremental, err := parser.Parse(full, oldTree)
			if err != nil {
				t.Fatalf("incremental parse: %v", err)
			}
			defer incremental.Close()

			fresh, err := Parse(tc.language, full)
			if err != nil {
				t.Fatalf("fresh parse: %v", err)
			}
			defer fresh.Close()

			if got, want := sexp(incremental.RootNode()), sexp(fresh.RootNode()); got != want {
				t.Fatalf("incremental tree differs from fresh:\n got=%s\nwant=%s", got, want)
			}
			if incremental.RootNode().HasError() {
				t.Fatal("incremental tree has parse errors")
			}

			ranges, err := ChangedRanges(oldTree, incremental)
			if err != nil {
				t.Fatalf("ChangedRanges: %v", err)
			}
			if len(ranges) == 0 {
				t.Fatal("expected non-empty changed ranges for an append")
			}
			for _, r := range ranges {
				if r.EndByte > uint32(len(full)) || r.StartByte > r.EndByte {
					t.Fatalf("changed range %+v out of bounds for %d-byte source", r, len(full))
				}
			}
		})
	}
}

func TestIncrementalParseHandlesMultibyteUnicode(t *testing.T) {
	t.Parallel()
	// A multi-byte rune (é, emoji) precedes the edit; byte offsets must stay correct.
	base := []byte("package s\n// café 🚀\nfunc a() {}\n")
	full := []byte(string(base) + "func b() {}\n")
	parser, err := NewParser(LanguageGo)
	if err != nil {
		t.Fatal(err)
	}
	defer parser.Close()
	oldTree, err := parser.Parse(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer oldTree.Close()
	if err := oldTree.Edit(appendEdit(base, full)); err != nil {
		t.Fatal(err)
	}
	incremental, err := parser.Parse(full, oldTree)
	if err != nil {
		t.Fatal(err)
	}
	defer incremental.Close()
	fresh, err := Parse(LanguageGo, full)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if sexp(incremental.RootNode()) != sexp(fresh.RootNode()) {
		t.Fatal("multibyte incremental parse differs from fresh")
	}
}

func TestInputEditValidationRejectsMalformed(t *testing.T) {
	t.Parallel()
	tree, err := Parse(LanguageGo, []byte("package s\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	bad := []InputEdit{
		{StartByte: 5, OldEndByte: 2},                // start > old end
		{StartByte: 5, OldEndByte: 6, NewEndByte: 2}, // start > new end
		{StartByte: 0, OldEndByte: 0, NewEndByte: 0, StartPoint: Point{Row: 2}, OldEndPoint: Point{Row: 1}}, // points inverted
		{StartByte: 0, OldEndByte: 9999, NewEndByte: 9999},                                                  // beyond tree size
	}
	for i, edit := range bad {
		if err := tree.Edit(edit); err == nil {
			t.Fatalf("case %d: malformed edit accepted", i)
		}
	}
}

func TestLifecycleIsSafe(t *testing.T) {
	t.Parallel()
	parser, err := NewParser(LanguageGo)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := parser.Parse([]byte("package s\n"), nil)
	if err != nil {
		t.Fatal(err)
	}

	root := tree.RootNode()
	// Tree.Close and Parser.Close are idempotent.
	tree.Close()
	tree.Close()
	if err := tree.Edit(appendEdit([]byte("package s\n"), []byte("package s\nfunc a(){}\n"))); err != ErrClosed {
		t.Fatalf("Edit after Close = %v, want ErrClosed", err)
	}
	if !tree.RootNode().IsNull() {
		t.Fatal("RootNode after Close should be null")
	}
	if !root.IsNull() || root.Type() != "" || root.StartByte() != 0 || root.EndByte() != 0 ||
		root.StartPoint() != (Point{}) || root.EndPoint() != (Point{}) || root.HasError() || root.IsMissing() ||
		root.NamedChildCount() != 0 || root.ChildCount() != 0 || !root.NamedChild(0).IsNull() ||
		!root.Child(0).IsNull() || !root.Parent().IsNull() || !root.PrevNamedSibling().IsNull() ||
		!root.ChildByFieldName("name").IsNull() || !root.NamedDescendantForPointRange(Point{}, Point{}).IsNull() ||
		root.Text([]byte("package s\n")) != "" {
		t.Fatal("node captured before Tree.Close did not fail closed")
	}

	// Parsing with a different language's tree is rejected.
	other, _ := Parse(LanguageJavaScript, []byte("function a(){}\n"))
	defer other.Close()
	if _, err := parser.Parse([]byte("package s\n"), other); err == nil {
		t.Fatal("incremental parse with mismatched language should fail")
	}

	if err := parser.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := parser.Close(); err != nil {
		t.Fatalf("second Close should be a no-op, got %v", err)
	}
	if _, err := parser.Parse([]byte("package s\n"), nil); err != ErrClosed {
		t.Fatalf("Parse after Close = %v, want ErrClosed", err)
	}
}
