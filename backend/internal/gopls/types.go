package gopls

import "github.com/ThalesMMS/CodeAtlas/internal/lspfacts"

// --- initialize ---

type initializeParams struct {
	ProcessID             *int                  `json:"processId"`
	RootURI               string                `json:"rootUri"`
	ClientInfo            clientInfo            `json:"clientInfo"`
	WorkspaceFolders      []workspaceFolder     `json:"workspaceFolders"`
	InitializationOptions initializationOptions `json:"initializationOptions"`
	Capabilities          clientCapabilities    `json:"capabilities"`
}

type initializationOptions struct {
	SemanticTokens bool `json:"semanticTokens"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type clientCapabilities struct {
	General      generalCapabilities            `json:"general"`
	TextDocument textDocumentClientCapabilities `json:"textDocument"`
}

type generalCapabilities struct {
	PositionEncodings []string `json:"positionEncodings"`
}

type textDocumentClientCapabilities struct {
	Synchronization syncCapabilities                `json:"synchronization"`
	Hover           *hoverClientCapabilities        `json:"hover,omitempty"`
	Definition      *emptyCapability                `json:"definition,omitempty"`
	References      *emptyCapability                `json:"references,omitempty"`
	Implementation  *emptyCapability                `json:"implementation,omitempty"`
	CallHierarchy   *emptyCapability                `json:"callHierarchy,omitempty"`
	SemanticTokens  semanticTokenClientCapabilities `json:"semanticTokens"`
}

type semanticTokenClientCapabilities struct {
	DynamicRegistration     bool                  `json:"dynamicRegistration"`
	Requests                semanticTokenRequests `json:"requests"`
	TokenTypes              []string              `json:"tokenTypes"`
	TokenModifiers          []string              `json:"tokenModifiers"`
	Formats                 []string              `json:"formats"`
	OverlappingTokenSupport bool                  `json:"overlappingTokenSupport"`
	MultilineTokenSupport   bool                  `json:"multilineTokenSupport"`
}

type semanticTokenRequests struct {
	Range bool `json:"range"`
	Full  bool `json:"full"`
}

type syncCapabilities struct {
	DidSave bool `json:"didSave"`
}

type hoverClientCapabilities struct{}
type emptyCapability struct{}

type initializeResult = lspfacts.InitializeResult
type serverCapabilities = lspfacts.ServerCapabilities
type serverInfo = lspfacts.ServerInfo
type providerOption = lspfacts.ProviderOption

// --- document sync ---

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int64  `json:"version"`
	Text       string `json:"text"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int64  `json:"version"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type contentChange struct {
	Text string `json:"text"` // full-document change (TextDocumentSyncKind.Full)
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

type didSaveParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Text         string                 `json:"text,omitempty"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}
