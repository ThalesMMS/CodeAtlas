package observability

import (
	"strings"
	"testing"
)

func TestRedactionCanariesNeverSurvive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		fn     func(string) string
		input  string
		canary string
	}{
		{"api_key assignment", RedactString, "dial failed: api_key=sk-SUPER-SECRET-VALUE-XYZ", "sk-SUPER-SECRET-VALUE-XYZ"},
		{"bearer header text", RedactString, "Authorization: Bearer sk-tok-abc123def456ghi", "sk-tok-abc123def456ghi"},
		{"bare sk key", RedactString, "leaked token sk-abcdef1234567890 in body", "sk-abcdef1234567890"},
		{"secret assignment", RedactString, "config secret=hunter2hunter2", "hunter2hunter2"},
		{"url userinfo", RedactURL, "https://user:secretpass@host:8000/v1/chat", "secretpass"},
		{"url query token", RedactURL, "https://host/v1?token=tok-QUERY-SECRET-123&x=1", "tok-QUERY-SECRET-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.fn(tc.input)
			if strings.Contains(out, tc.canary) {
				t.Fatalf("redaction leaked canary %q: %s", tc.canary, out)
			}
		})
	}
}

func TestRedactURLKeepsHostAndPath(t *testing.T) {
	t.Parallel()
	out := RedactURL("https://user:pw@api.host:8000/v1/chat/completions?token=abc")
	if !strings.Contains(out, "api.host:8000") || !strings.Contains(out, "/v1/chat/completions") {
		t.Fatalf("RedactURL dropped useful diagnostics: %s", out)
	}
}

func TestRedactPathIsRelativeAndTraversalFree(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/abs/secret/file.go": "abs/secret/file.go",
		"../../etc/passwd":    "etc/passwd",
		"a/../b/c.go":         "b/c.go",
		"./pkg/svc.go":        "pkg/svc.go",
	}
	for input, want := range cases {
		if got := RedactPath(input); got != want {
			t.Errorf("RedactPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHash8AndShortHash(t *testing.T) {
	t.Parallel()
	if got := Hash8([]byte("hello world")); len(got) != 8 {
		t.Fatalf("Hash8 length = %d, want 8", len(got))
	}
	if got := ShortHash("sha256:" + strings.Repeat("a", 64)); got != "aaaaaaaa" {
		t.Fatalf("ShortHash = %q, want 8 a's", got)
	}
}

func TestQueryShapeDoesNotRevealText(t *testing.T) {
	t.Parallel()
	length, hash := QueryShape("submit secret order")
	if length != len([]rune("submit secret order")) {
		t.Fatalf("length = %d", length)
	}
	if strings.Contains(hash, "secret") || len(hash) != 8 {
		t.Fatalf("hash leaks or is wrong length: %q", hash)
	}
}
