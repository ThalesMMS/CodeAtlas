package treesitter

import "testing"

func TestParseSupportedLanguages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		language Language
		source   string
		rootType string
	}{
		{name: "Go", language: LanguageGo, source: "package sample\nfunc main() {}\n", rootType: "source_file"},
		{name: "JavaScript", language: LanguageJavaScript, source: "export function run() { return 1 }\n", rootType: "program"},
		{name: "TypeScript", language: LanguageTypeScript, source: "export const run = (id: string): string => id\n", rootType: "program"},
		{name: "TSX", language: LanguageTSX, source: "export const App = () => <main>Hello</main>\n", rootType: "program"},
		{name: "Swift", language: LanguageSwift, source: "struct Order { let id: String }\n", rootType: "source_file"},
		{name: "Python", language: LanguagePython, source: "class Order:\n    id: str\n", rootType: "module"},
		{name: "Rust", language: LanguageRust, source: "struct Order { id: String }\n", rootType: "source_file"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tree, err := Parse(test.language, []byte(test.source))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			defer tree.Close()
			root := tree.RootNode()
			if root.IsNull() {
				t.Fatal("root node is null")
			}
			if got := root.Type(); got != test.rootType {
				t.Fatalf("root.Type() = %q, want %q", got, test.rootType)
			}
		})
	}
}

func TestParseRejectsUnknownLanguage(t *testing.T) {
	if _, err := Parse(Language("unknown"), []byte("x")); err == nil {
		t.Fatal("Parse() expected an unsupported-language error")
	}
}
