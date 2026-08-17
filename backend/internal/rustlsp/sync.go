package rustlsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

type initializeResult = lspfacts.InitializeResult
type serverCapabilities = lspfacts.ServerCapabilities
type providerOption = lspfacts.ProviderOption

var (
	ErrUnavailable             = errors.New("rust-analyzer: unavailable")
	ErrDocumentNotOpen         = errors.New("rust-analyzer: document not open")
	ErrStaleVersion            = semantic.ErrProviderStale
	ErrProviderRestarted       = semantic.ErrProviderRestarted
	ErrMalformedSemanticTokens = semantic.ErrProviderPayloadInvalid
)

// Document is the boundary view of an overlay to sync.
type Document struct {
	DocumentID string
	Path       string
	Version    int64
	Content    []byte
}

// Capabilities reports the negotiated semantic capabilities.
func (m *Manager) Capabilities() semantic.SemanticCapabilities {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decision != DecisionEnabled {
		return semantic.SemanticCapabilities{}
	}
	capabilities := semantic.SemanticCapabilities{
		Hover:            m.serverCaps.HoverProvider.Present(),
		Definition:       m.serverCaps.DefinitionProvider.Present(),
		References:       m.serverCaps.ReferencesProvider.Present(),
		Implementation:   m.serverCaps.ImplementationProvider.Present(),
		CallHierarchy:    m.serverCaps.CallHierarchyProvider.Present(),
		Diagnostics:      true,
		DocumentSyncKind: "full",
		PositionEncoding: m.encoding,
	}
	if m.serverCaps.SemanticTokensProvider.FullPresent() {
		capabilities.SemanticTokensFull = true
		capabilities.SemanticTokenTypes = append([]string(nil), semantic.CanonicalSemanticTokenTypes...)
		capabilities.SemanticTokenModifiers = append([]string(nil), semantic.CanonicalSemanticTokenModifiers...)
	}
	return capabilities
}

func (m *Manager) semanticTokenLegend() lspfacts.SemanticTokensLegend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return lspfacts.SemanticTokensLegend{
		TokenTypes:     append([]string(nil), m.serverCaps.SemanticTokensProvider.Legend.TokenTypes...),
		TokenModifiers: append([]string(nil), m.serverCaps.SemanticTokensProvider.Legend.TokenModifiers...),
	}
}

func (m *Manager) operational() (LSPClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decision != DecisionEnabled || m.client == nil {
		return nil, ErrUnavailable
	}
	if state := m.client.State(); state == lspclient.StateFailed || state == lspclient.StateClosed {
		m.decision = DecisionUnavailable
		m.reason = CodeCrashed
		m.documents.Reset()
		m.diagnostics.Reset()
		return nil, ErrUnavailable
	}
	return m.client, nil
}

func (m *Manager) setSynced(documentID string, version int64) {
	m.documents.Set(documentID, version)
}

// SyncedVersion returns the version the server last accepted for a document.
func (m *Manager) SyncedVersion(documentID string) (int64, bool) {
	return m.documents.Version(documentID)
}

// Open sends didOpen with the correct Rust languageId.
func (m *Manager) Open(ctx context.Context, doc Document) error {
	client, err := m.operational()
	if err != nil {
		return err
	}
	unlock, err := m.documents.Lock(ctx, doc.DocumentID)
	if err != nil {
		return err
	}
	defer unlock()
	params := map[string]any{"textDocument": map[string]any{
		"uri": m.documentURI(doc.Path), "languageId": LanguageID(doc.Path), "version": doc.Version, "text": string(doc.Content),
	}}
	if err := client.Notify(ctx, "textDocument/didOpen", params); err != nil {
		return fmt.Errorf("rust didOpen: %w", err)
	}
	m.setSynced(doc.DocumentID, doc.Version)
	return nil
}

// Change sends a Full didChange for a strictly advancing version, serialized per document.
func (m *Manager) Change(ctx context.Context, doc Document) error {
	client, err := m.operational()
	if err != nil {
		return err
	}
	unlock, err := m.documents.Lock(ctx, doc.DocumentID)
	if err != nil {
		return err
	}
	defer unlock()
	current, ok := m.SyncedVersion(doc.DocumentID)
	if !ok {
		return ErrDocumentNotOpen
	}
	if doc.Version <= current {
		return ErrStaleVersion
	}
	params := map[string]any{
		"textDocument":   map[string]any{"uri": m.documentURI(doc.Path), "version": doc.Version},
		"contentChanges": []map[string]any{{"text": string(doc.Content)}},
	}
	if err := client.Notify(ctx, "textDocument/didChange", params); err != nil {
		return fmt.Errorf("rust didChange: %w", err)
	}
	m.setSynced(doc.DocumentID, doc.Version)
	m.diagnostics.InvalidateUnknown(m.documentURI(doc.Path))
	return nil
}

// Save sends didSave after the file+index commit.
func (m *Manager) Save(ctx context.Context, doc Document) error {
	client, err := m.operational()
	if err != nil {
		return err
	}
	unlock, err := m.documents.Lock(ctx, doc.DocumentID)
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := m.SyncedVersion(doc.DocumentID); !ok {
		return ErrDocumentNotOpen
	}
	return client.Notify(ctx, "textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": m.documentURI(doc.Path)}})
}

// Close sends didClose and forgets the version, proceeding even on failure.
func (m *Manager) Close(ctx context.Context, doc Document) error {
	uri := m.documentURI(doc.Path)
	client, err := m.operational()
	if err != nil {
		m.documents.Clear(doc.DocumentID)
		m.diagnostics.Clear(uri)
		return err
	}
	unlock, err := m.documents.Lock(ctx, doc.DocumentID)
	if err != nil {
		m.documents.Clear(doc.DocumentID)
		m.diagnostics.Clear(uri)
		return err
	}
	defer unlock()
	notifyErr := client.Notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	m.documents.Clear(doc.DocumentID)
	m.diagnostics.Clear(uri)
	return notifyErr
}

func (m *Manager) documentURI(path string) string {
	return lspconv.PathToURI(filepath.Join(m.workspaceRoot, path))
}

// LanguageID maps a path to the LSP language id for the Rust server.
func LanguageID(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".rs") {
		return "rust"
	}
	return "plaintext"
}
