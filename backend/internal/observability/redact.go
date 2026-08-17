package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// sensitiveQueryKeys are query parameters whose values are stripped from a
// logged URL.
var sensitiveQueryKeys = map[string]struct{}{
	"token": {}, "key": {}, "api_key": {}, "apikey": {}, "secret": {}, "password": {}, "access_token": {},
}

// bearerPattern and apiKeyPattern catch the most common credential shapes that
// might otherwise reach a log line through an error message.
var (
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`)
	apiKeyPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password)\s*[=:]\s*[^\s"'&]+`)
	skKeyPattern  = regexp.MustCompile(`sk-[A-Za-z0-9]{8,}`)
)

// RedactString removes credential-like substrings from free text (e.g. an
// upstream error body) before it is logged. It is deliberately conservative:
// it never returns the original secret.
func RedactString(value string) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = skKeyPattern.ReplaceAllString(value, "[REDACTED]")
	value = apiKeyPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + " [REDACTED]"
		}
		return "[REDACTED]"
	})
	return value
}

// RedactURL strips userinfo and sensitive query parameter values, keeping the
// scheme/host/path for diagnostics. A non-URL string is redacted as free text.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return RedactString(raw)
	}
	if parsed.User != nil {
		parsed.User = url.User("[REDACTED]")
	}
	query := parsed.Query()
	for key := range query {
		if _, sensitive := sensitiveQueryKeys[strings.ToLower(key)]; sensitive {
			query.Set(key, "[REDACTED]")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// RedactPath returns a clean, relative, traversal-free form of a workspace path
// safe to log. It never reveals an absolute filesystem location.
func RedactPath(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	clean = strings.TrimPrefix(clean, "/")
	parts := make([]string, 0)
	for _, part := range strings.Split(clean, "/") {
		if part == ".." || part == "." || part == "" {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "/")
}

// Hash8 returns the first 8 hex chars of the SHA-256 of content, a safe short
// identifier that reveals nothing about the content itself.
func Hash8(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])[:8]
}

// ShortHash truncates an already-computed hash (optionally "sha256:"-prefixed)
// to a short, log-safe form.
func ShortHash(hash string) string {
	trimmed := strings.TrimPrefix(hash, "sha256:")
	if len(trimmed) > 8 {
		return trimmed[:8]
	}
	return trimmed
}

// QueryShape describes a search query without revealing it: its length and a
// short content hash. High-cardinality raw text never reaches a log or metric.
func QueryShape(query string) (length int, hash string) {
	return len([]rune(query)), Hash8([]byte(query))
}
