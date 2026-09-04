package desktop

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/observability"
)

const maxFatalHTMLBytes = 8 * 1024

var fatalURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

func FatalHTML(title string, err error) string {
	safeTitle := html.EscapeString(truncateUTF8(observability.RedactString(title), 512))
	detail := "An unknown error occurred."
	if err != nil {
		detail = safeFatalText(err.Error())
	}
	escapedDetail := html.EscapeString(detail)

	prefix := "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><title>" + safeTitle + "</title><style>html{color-scheme:dark;background:#111827;color:#e5e7eb;font:16px system-ui,sans-serif}body{margin:0;min-height:100vh;display:grid;place-items:center}.card{width:min(42rem,calc(100% - 3rem));padding:2rem;border:1px solid #374151;border-radius:.75rem;background:#1f2937}h1{margin-top:0;font-size:1.35rem}pre{white-space:pre-wrap;overflow-wrap:anywhere;color:#d1d5db}</style></head><body><main class=\"card\"><h1>" + safeTitle + "</h1><p>CodeAtlas could not start or stopped unexpectedly.</p><pre>"
	suffix := "</pre><p>Close this window after reviewing the error.</p></main></body></html>"
	remaining := maxFatalHTMLBytes - len(prefix) - len(suffix)
	if remaining < 0 {
		remaining = 0
	}
	escapedDetail = truncateUTF8(escapedDetail, remaining)
	return prefix + escapedDetail + suffix
}

func safeFatalText(value string) string {
	redacted := observability.RedactString(value)
	return fatalURLPattern.ReplaceAllStringFunc(redacted, observability.RedactURL)
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = strings.TrimSuffix(value, value[len(value)-1:])
	}
	return value
}

// RestartingHTML is the placeholder shown in the native window while the
// composition is torn down and started again after a settings restart. The
// controller navigates to the new listener as soon as it is bound.
func RestartingHTML() string {
	return "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><title>Restarting CodeAtlas</title><style>html{color-scheme:dark;background:#111827;color:#e5e7eb;font:16px system-ui,sans-serif}body{margin:0;min-height:100vh;display:grid;place-items:center}.card{width:min(42rem,calc(100% - 3rem));padding:2rem;border:1px solid #374151;border-radius:.75rem;background:#1f2937;text-align:center}h1{margin-top:0;font-size:1.35rem}p{color:#d1d5db}</style></head><body><main class=\"card\"><h1>Restarting CodeAtlas…</h1><p>Applying the saved settings and reopening the workspace.</p></main></body></html>"
}
