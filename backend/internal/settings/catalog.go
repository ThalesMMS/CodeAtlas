package settings

// FieldKey is the stable environment-variable identity used by persistence,
// HTTP DTOs, validation errors, and the browser field inventory.
type FieldKey string

// DefaultListenAddress uses a high, non-standard port to avoid colliding with
// common local development servers and bundled services such as whisper.cpp.
const DefaultListenAddress = "127.0.0.1:43127"

const (
	FieldWorkspace          FieldKey = "CODEATLAS_WORKSPACE"
	FieldListen             FieldKey = "CODEATLAS_LISTEN"
	FieldMaxFileBytes       FieldKey = "CODEATLAS_MAX_FILE_BYTES"
	FieldLLMBaseURL         FieldKey = "CODEATLAS_LLM_BASE_URL"
	FieldLLMAPIKey          FieldKey = "CODEATLAS_LLM_API_KEY"
	FieldLLMModel           FieldKey = "CODEATLAS_LLM_MODEL"
	FieldLLMReasoningEffort FieldKey = "CODEATLAS_LLM_REASONING_EFFORT"
	FieldLLMTimeout         FieldKey = "CODEATLAS_LLM_TIMEOUT"
	FieldGoplsMode          FieldKey = "CODEATLAS_GOPLS"
	FieldGoplsPath          FieldKey = "CODEATLAS_GOPLS_PATH"
	FieldTypeScriptLSPMode  FieldKey = "CODEATLAS_TYPESCRIPT_LSP"
	FieldTypeScriptLSPPath  FieldKey = "CODEATLAS_TYPESCRIPT_LSP_PATH"
	FieldTypeScriptSDKPath  FieldKey = "CODEATLAS_TYPESCRIPT_SDK_PATH"
	FieldSwiftLSPMode       FieldKey = "CODEATLAS_SWIFT_LSP"
	FieldSwiftLSPPath       FieldKey = "CODEATLAS_SWIFT_LSP_PATH"
	FieldPythonLSPMode      FieldKey = "CODEATLAS_PYTHON_LSP"
	FieldPythonLSPPath      FieldKey = "CODEATLAS_PYTHON_LSP_PATH"
	FieldRustLSPMode        FieldKey = "CODEATLAS_RUST_LSP"
	FieldRustLSPPath        FieldKey = "CODEATLAS_RUST_LSP_PATH"
	FieldEnableEmbeddings   FieldKey = "CODEATLAS_ENABLE_EMBEDDINGS"
	FieldEmbeddingModel     FieldKey = "CODEATLAS_EMBEDDING_MODEL"
	FieldEmbeddingBaseURL   FieldKey = "CODEATLAS_EMBEDDING_BASE_URL"
	FieldEmbeddingsAPIKey   FieldKey = "CODEATLAS_EMBEDDINGS_API_KEY"
)

type Group string

const (
	GroupGeneral         Group = "general"
	GroupLLM             Group = "llm"
	GroupEmbeddings      Group = "embeddings"
	GroupLanguageServers Group = "languageServers"
)

type ApplyMode string

const (
	ApplyLive    ApplyMode = "live"
	ApplyRestart ApplyMode = "restart"
)

type ValueKind string

const (
	KindString   ValueKind = "string"
	KindInteger  ValueKind = "integer"
	KindBoolean  ValueKind = "boolean"
	KindDuration ValueKind = "duration"
	KindSecret   ValueKind = "secret"
)

type FieldDefinition struct {
	Key        FieldKey
	Group      Group
	Secret     bool
	ApplyMode  ApplyMode
	Default    string
	Kind       ValueKind
	AllowEmpty bool
}

var documentedFields = []FieldDefinition{
	{Key: FieldWorkspace, Group: GroupGeneral, ApplyMode: ApplyRestart, Default: ".", Kind: KindString},
	{Key: FieldListen, Group: GroupGeneral, ApplyMode: ApplyRestart, Default: DefaultListenAddress, Kind: KindString},
	{Key: FieldMaxFileBytes, Group: GroupGeneral, ApplyMode: ApplyRestart, Default: "1500000", Kind: KindInteger},
	{Key: FieldLLMBaseURL, Group: GroupLLM, ApplyMode: ApplyLive, Kind: KindString},
	{Key: FieldLLMAPIKey, Group: GroupLLM, Secret: true, ApplyMode: ApplyLive, Kind: KindSecret, AllowEmpty: true},
	{Key: FieldLLMModel, Group: GroupLLM, ApplyMode: ApplyLive, Kind: KindString},
	{Key: FieldLLMReasoningEffort, Group: GroupLLM, ApplyMode: ApplyLive, Kind: KindString, AllowEmpty: true},
	{Key: FieldLLMTimeout, Group: GroupLLM, ApplyMode: ApplyLive, Default: "10m", Kind: KindDuration},
	{Key: FieldGoplsMode, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "auto", Kind: KindString},
	{Key: FieldGoplsPath, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "gopls", Kind: KindString},
	{Key: FieldTypeScriptLSPMode, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "auto", Kind: KindString},
	{Key: FieldTypeScriptLSPPath, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "typescript-language-server", Kind: KindString},
	{Key: FieldTypeScriptSDKPath, Group: GroupLanguageServers, ApplyMode: ApplyLive, Kind: KindString, AllowEmpty: true},
	{Key: FieldSwiftLSPMode, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "auto", Kind: KindString},
	{Key: FieldSwiftLSPPath, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "sourcekit-lsp", Kind: KindString},
	{Key: FieldPythonLSPMode, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "auto", Kind: KindString},
	{Key: FieldPythonLSPPath, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "pyright-langserver", Kind: KindString},
	{Key: FieldRustLSPMode, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "auto", Kind: KindString},
	{Key: FieldRustLSPPath, Group: GroupLanguageServers, ApplyMode: ApplyLive, Default: "rust-analyzer", Kind: KindString},
	{Key: FieldEnableEmbeddings, Group: GroupEmbeddings, ApplyMode: ApplyLive, Default: "false", Kind: KindBoolean},
	{Key: FieldEmbeddingModel, Group: GroupEmbeddings, ApplyMode: ApplyLive, Kind: KindString, AllowEmpty: true},
	{Key: FieldEmbeddingBaseURL, Group: GroupEmbeddings, ApplyMode: ApplyLive, Kind: KindString, AllowEmpty: true},
	{Key: FieldEmbeddingsAPIKey, Group: GroupEmbeddings, Secret: true, ApplyMode: ApplyLive, Kind: KindSecret, AllowEmpty: true},
}

func DocumentedFields() []FieldDefinition {
	return append([]FieldDefinition(nil), documentedFields...)
}

func Definition(key FieldKey) (FieldDefinition, bool) {
	for _, definition := range documentedFields {
		if definition.Key == key {
			return definition, true
		}
	}
	return FieldDefinition{}, false
}
