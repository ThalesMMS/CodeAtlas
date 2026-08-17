package lspadapter

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

const (
	maxDiagnosticsPerURI = 200
	maxDiagnosticMessage = 1000
)

// DiagnosticSet is a version-aware snapshot of diagnostics for one URI.
type DiagnosticSet struct {
	Version      int64
	VersionKnown bool
	Items        []lspfacts.Diagnostic
}

// DiagnosticStore bounds, versions and invalidates publishDiagnostics payloads.
type DiagnosticStore struct {
	mu    sync.Mutex
	byURI map[string]DiagnosticSet
}

func NewDiagnosticStore() *DiagnosticStore {
	return &DiagnosticStore{byURI: make(map[string]DiagnosticSet)}
}

func (s *DiagnosticStore) Ingest(params json.RawMessage) {
	var payload lspfacts.PublishDiagnosticsParams
	if err := json.Unmarshal(params, &payload); err != nil || payload.URI == "" {
		return
	}
	items := payload.Diagnostics
	if len(items) > maxDiagnosticsPerURI {
		items = items[:maxDiagnosticsPerURI]
	}
	items = append([]lspfacts.Diagnostic(nil), items...)
	for index := range items {
		items[index].Message = lspfacts.BoundString(items[index].Message)
		if len(items[index].Message) > maxDiagnosticMessage {
			items[index].Message = textutil.TruncateUTF8(items[index].Message, maxDiagnosticMessage)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(items) == 0 {
		delete(s.byURI, payload.URI)
		return
	}
	set := DiagnosticSet{Items: items}
	if payload.Version != nil {
		set.Version = *payload.Version
		set.VersionKnown = true
	}
	s.byURI[payload.URI] = set
}

func (s *DiagnosticStore) InvalidateUnknown(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if set, exists := s.byURI[uri]; exists && !set.VersionKnown {
		delete(s.byURI, uri)
	}
}

func (s *DiagnosticStore) Get(uri string) (DiagnosticSet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, exists := s.byURI[uri]
	set.Items = append([]lspfacts.Diagnostic(nil), set.Items...)
	return set, exists
}

// Clear removes diagnostics for one document URI without disturbing other
// documents owned by the same provider session.
func (s *DiagnosticStore) Clear(uri string) {
	s.mu.Lock()
	delete(s.byURI, uri)
	s.mu.Unlock()
}

func (s *DiagnosticStore) Reset() {
	s.mu.Lock()
	s.byURI = make(map[string]DiagnosticSet)
	s.mu.Unlock()
}

// DiagnosticDetail renders one bounded protocol diagnostic consistently across
// language adapters.
func DiagnosticDetail(diagnostic lspfacts.Diagnostic) string {
	severity := map[int]string{1: "error", 2: "warning", 3: "info", 4: "hint"}[diagnostic.Severity]
	if severity == "" {
		severity = "diagnostic"
	}
	parts := []string{"[" + severity + "]"}
	if diagnostic.Source != "" {
		parts = append(parts, diagnostic.Source+":")
	}
	parts = append(parts, diagnostic.Message)
	return semantic.SanitizeDetail(strings.Join(parts, " "))
}

// NormalizeDiagnostic returns the bounded provider-neutral diagnostic payload.
func NormalizeDiagnostic(diagnostic lspfacts.Diagnostic) semantic.DiagnosticFact {
	severity := map[int]string{1: "error", 2: "warning", 3: "info", 4: "hint"}[diagnostic.Severity]
	if severity == "" {
		severity = "info"
	}
	code := ""
	if len(diagnostic.Code) > 0 {
		var asString string
		if json.Unmarshal(diagnostic.Code, &asString) == nil {
			code = lspfacts.BoundString(asString)
		} else {
			var asNumber json.Number
			if json.Unmarshal(diagnostic.Code, &asNumber) == nil {
				code = lspfacts.BoundString(asNumber.String())
			}
		}
	}
	return semantic.DiagnosticFact{
		Severity: severity,
		Code:     code,
		Source:   lspfacts.BoundString(diagnostic.Source),
		Message:  lspfacts.BoundString(diagnostic.Message),
	}
}
