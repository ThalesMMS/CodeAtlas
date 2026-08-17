// Package lspfacts holds the shared LSP wire types and parsing helpers used by
// every language-server adapter (gopls, the TypeScript server). It is
// language-neutral: position/URI conversion lives in lspconv and fact assembly in
// the adapters; this package only models the protocol shapes and normalizes the
// protocol's union responses (Location vs LocationLink, MarkupContent variants).
package lspfacts

import (
	"encoding/json"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type LocationLink struct {
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

type HoverResult struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}

type CallHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	URI            string `json:"uri"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
	// Data is opaque server state kept only for the immediate follow-up call.
	Data json.RawMessage `json:"data,omitempty"`
}

type IncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

type OutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int64       `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// InitializeResult and ServerCapabilities are the language-neutral subset read
// by every adapter during the initialize handshake.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ServerCapabilities struct {
	PositionEncoding       string                       `json:"positionEncoding"`
	TextDocumentSync       json.RawMessage              `json:"textDocumentSync"`
	HoverProvider          ProviderOption               `json:"hoverProvider"`
	DefinitionProvider     ProviderOption               `json:"definitionProvider"`
	ReferencesProvider     ProviderOption               `json:"referencesProvider"`
	ImplementationProvider ProviderOption               `json:"implementationProvider"`
	CallHierarchyProvider  ProviderOption               `json:"callHierarchyProvider"`
	SemanticTokensProvider SemanticTokensProviderOption `json:"semanticTokensProvider"`
}

type SemanticTokensLegend struct {
	TokenTypes     []string `json:"tokenTypes"`
	TokenModifiers []string `json:"tokenModifiers"`
}

// SemanticTokensProviderOption accepts the protocol object form and tolerates
// false/null. The raw provider legend stays inside the adapter boundary.
type SemanticTokensProviderOption struct {
	Legend  SemanticTokensLegend `json:"legend"`
	Full    json.RawMessage      `json:"full"`
	Range   json.RawMessage      `json:"range"`
	present bool
}

func (p *SemanticTokensProviderOption) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" || trimmed == "false" {
		*p = SemanticTokensProviderOption{}
		return nil
	}
	type wire SemanticTokensProviderOption
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = SemanticTokensProviderOption(value)
	p.present = true
	return nil
}

func (p SemanticTokensProviderOption) Present() bool { return p.present }

func (p SemanticTokensProviderOption) MarshalJSON() ([]byte, error) {
	if !p.present {
		return []byte("null"), nil
	}
	type wire SemanticTokensProviderOption
	return json.Marshal(wire(p))
}

func (p SemanticTokensProviderOption) FullPresent() bool {
	if !p.present {
		return false
	}
	value := strings.TrimSpace(string(p.Full))
	return value != "" && value != "false" && value != "null"
}

func NewSemanticTokensProviderOption(legend SemanticTokensLegend, full bool) SemanticTokensProviderOption {
	value := SemanticTokensProviderOption{Legend: legend, present: true}
	if full {
		value.Full = json.RawMessage("true")
	} else {
		value.Full = json.RawMessage("false")
	}
	return value
}

type SemanticTokens struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

// ProviderOption tolerates a capability reported as a bool, object or null.
type ProviderOption struct{ raw json.RawMessage }

func (p *ProviderOption) UnmarshalJSON(data []byte) error {
	p.raw = append(json.RawMessage(nil), data...)
	return nil
}

func (p ProviderOption) MarshalJSON() ([]byte, error) {
	if len(p.raw) == 0 {
		return []byte("null"), nil
	}
	return p.raw, nil
}

func (p ProviderOption) Present() bool {
	value := strings.TrimSpace(string(p.raw))
	return value != "" && value != "false" && value != "null"
}

func NewProviderOption(present bool) ProviderOption {
	if present {
		return ProviderOption{raw: json.RawMessage("true")}
	}
	return ProviderOption{raw: json.RawMessage("false")}
}

// Hover content limits keep an untrusted payload bounded.
const (
	MaxHoverBytes  = 2000
	MaxHoverBlocks = 8
)

// BoundHoverText extracts plain text from a hover Contents value (MarkupContent,
// MarkedString, or arrays thereof), bounded and treated as untrusted text — never
// rendered as raw HTML.
func BoundHoverText(contents json.RawMessage) string {
	if len(contents) == 0 {
		return ""
	}
	var markup struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(contents, &markup); err == nil && markup.Value != "" {
		return BoundString(markup.Value)
	}
	var asString string
	if err := json.Unmarshal(contents, &asString); err == nil && asString != "" {
		return BoundString(asString)
	}
	var asArray []json.RawMessage
	if err := json.Unmarshal(contents, &asArray); err == nil {
		parts := make([]string, 0, len(asArray))
		for i, item := range asArray {
			if i >= MaxHoverBlocks {
				break
			}
			if text := BoundHoverText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return BoundString(strings.Join(parts, "\n"))
	}
	return ""
}

func BoundString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > MaxHoverBytes {
		value = textutil.TruncateUTF8(value, MaxHoverBytes) + "…"
	}
	return value
}

// NormalizeLocations accepts the protocol's Location, Location[] or LocationLink[]
// shapes and returns a flat list of locations.
func NormalizeLocations(raw json.RawMessage) []Location {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var single Location
		if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
			return []Location{single}
		}
		var link LocationLink
		if err := json.Unmarshal(raw, &link); err == nil && link.TargetURI != "" {
			return []Location{linkToLocation(link)}
		}
		return nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return nil
	}
	locations := make([]Location, 0, len(rawItems))
	for _, item := range rawItems {
		var loc Location
		if err := json.Unmarshal(item, &loc); err == nil && loc.URI != "" {
			locations = append(locations, loc)
			continue
		}
		var link LocationLink
		if err := json.Unmarshal(item, &link); err == nil && link.TargetURI != "" {
			locations = append(locations, linkToLocation(link))
		}
	}
	return locations
}

func linkToLocation(link LocationLink) Location {
	rng := link.TargetSelectionRange
	if rng == (Range{}) {
		rng = link.TargetRange
	}
	return Location{URI: link.TargetURI, Range: rng}
}

// SelectionOf returns an item's selection range, falling back to its full range.
func SelectionOf(item CallHierarchyItem) Range {
	if item.SelectionRange != (Range{}) {
		return item.SelectionRange
	}
	return item.Range
}
