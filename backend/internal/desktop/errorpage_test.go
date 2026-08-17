package desktop

import (
	"errors"
	"strings"
	"testing"
)

func TestFatalHTMLRedactsEscapesAndBoundsErrors(t *testing.T) {
	detail := "api_key=sk-DESKTOP-SECRET https://user:password@example.invalid/v1 <script>alert(1)</script> " + strings.Repeat("x", 20_000)
	html := FatalHTML("CodeAtlas <failed>", errors.New(detail))

	for _, secret := range []string{"sk-DESKTOP-SECRET", "password", "<script", "<failed>"} {
		if strings.Contains(html, secret) {
			t.Fatalf("FatalHTML leaked %q: %s", secret, html)
		}
	}
	if !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "CodeAtlas &lt;failed&gt;") {
		t.Fatalf("FatalHTML did not escape content: %s", html)
	}
	if len(html) > 8*1024 {
		t.Fatalf("FatalHTML length = %d, want <= 8192", len(html))
	}
}
