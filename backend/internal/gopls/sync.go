package gopls

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/lspconv"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

var (
	// ErrUnavailable means gopls is not operational; the caller must not declare a
	// document semantically ready.
	ErrUnavailable = errors.New("gopls: unavailable")
	// ErrDocumentNotOpen means a change/save/close arrived for an unopened document.
	ErrDocumentNotOpen = errors.New("gopls: document not open")
	// ErrStaleVersion means a non-advancing document version was offered.
	ErrStaleVersion            = semantic.ErrProviderStale
	ErrProviderRestarted       = semantic.ErrProviderRestarted
	ErrMalformedSemanticTokens = semantic.ErrProviderPayloadInvalid
)

// Document is the boundary view of an overlay the manager needs to sync. The
// caller populates it from an OverlayStore snapshot.
type Document struct {
	DocumentID string
	Path       string
	LanguageID string
	Version    int64
	Content    []byte
}

// Capabilities reports the negotiated semantic capabilities for /api/capabilities.
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
		Diagnostics:      true, // gopls publishes diagnostics
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

// operational returns the running client or marks the manager crashed and nil.
func (m *Manager) operational() (LSPClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decision != DecisionEnabled || m.client == nil {
		return nil, ErrUnavailable
	}
	if m.client.State() == lspclient.StateFailed || m.client.State() == lspclient.StateClosed {
		// A crashed transport flips the capability to unavailable and drops sync.
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

func (m *Manager) clearSynced(documentID string) {
	m.documents.Clear(documentID)
}

// SyncedVersion returns the version gopls last accepted for a document.
func (m *Manager) SyncedVersion(documentID string) (int64, bool) {
	return m.documents.Version(documentID)
}

// Open sends didOpen after the overlay and parse session are ready. A failed
// didOpen leaves the document not synced, so the caller does not declare it ready.
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
	params := didOpenParams{TextDocument: textDocumentItem{
		URI: m.documentURI(doc.Path), LanguageID: languageID(doc.LanguageID), Version: doc.Version, Text: string(doc.Content),
	}}
	if err := client.Notify(ctx, "textDocument/didOpen", params); err != nil {
		return fmt.Errorf("gopls didOpen: %w", err)
	}
	m.setSynced(doc.DocumentID, doc.Version)
	return nil
}

// Change sends a full-document didChange for a strictly advancing version; it
// never sends a version not yet acknowledged by the backend and serializes per
// document.
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
	params := didChangeParams{
		TextDocument:   versionedTextDocumentIdentifier{URI: m.documentURI(doc.Path), Version: doc.Version},
		ContentChanges: []contentChange{{Text: string(doc.Content)}},
	}
	if err := client.Notify(ctx, "textDocument/didChange", params); err != nil {
		return fmt.Errorf("gopls didChange: %w", err)
	}
	// SyncedVersion only advances after the transport accepted the notification.
	m.setSynced(doc.DocumentID, doc.Version)
	// Version-unknown diagnostics for this document can no longer be trusted.
	m.invalidateUnknownDiagnostics(m.documentURI(doc.Path))
	return nil
}

// Save sends didSave after the file+index commit and MarkSaved.
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
	return client.Notify(ctx, "textDocument/didSave", didSaveParams{TextDocument: textDocumentIdentifier{URI: m.documentURI(doc.Path)}})
}

// Close sends didClose and forgets the synced version. It proceeds even if the
// notification fails.
func (m *Manager) Close(ctx context.Context, doc Document) error {
	client, err := m.operational()
	if err != nil {
		m.clearSynced(doc.DocumentID)
		return err
	}
	unlock, err := m.documents.Lock(ctx, doc.DocumentID)
	if err != nil {
		m.clearSynced(doc.DocumentID)
		return err
	}
	defer unlock()
	notifyErr := client.Notify(ctx, "textDocument/didClose", didCloseParams{TextDocument: textDocumentIdentifier{URI: m.documentURI(doc.Path)}})
	m.clearSynced(doc.DocumentID)
	return notifyErr
}

func (m *Manager) documentURI(path string) string {
	return lspconv.PathToURI(filepath.Join(m.workspaceRoot, path))
}

func languageID(id string) string {
	if id == "" {
		return "go"
	}
	return id
}
