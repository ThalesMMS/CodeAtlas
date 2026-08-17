package pathutil

import (
	"errors"
	"testing"
)

func TestNormalizeWorkspaceRelative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "unchanged", input: "pkg/app.go", want: "pkg/app.go"},
		{name: "trim and clean", input: "  pkg/./nested/../app.go  ", want: "pkg/app.go"},
		{name: "backslashes", input: `pkg\\nested\\app.go`, want: "pkg/nested/app.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeWorkspaceRelative(test.input)
			if err != nil {
				t.Fatalf("NormalizeWorkspaceRelative(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("NormalizeWorkspaceRelative(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeWorkspaceRelativeRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"", "   ", ".", "..", "../secret.go", `..\\secret.go`, "/etc/passwd",
		`C:\\Windows\\system.ini`, "https://example.test/file.go", "pkg/\x00file.go",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if got, err := NormalizeWorkspaceRelative(input); !errors.Is(err, ErrInvalidWorkspaceRelative) || got != "" {
				t.Fatalf("NormalizeWorkspaceRelative(%q) = %q, %v; want empty ErrInvalidWorkspaceRelative", input, got, err)
			}
		})
	}
}
