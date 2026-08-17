package settings

import (
	"fmt"
	"time"
)

type Source string

const (
	SourceDefault  Source = "default"
	SourceEnv      Source = "env"
	SourceSettings Source = "settings"
	SourceNone     Source = "none"
)

type Environment map[FieldKey]string
type Overrides map[FieldKey]any
type SecretValues map[FieldKey]string

type Values struct {
	Workspace          string
	ListenAddress      string
	MaxFileBytes       int64
	LLMBaseURL         string
	LLMAPIKey          string
	LLMModel           string
	LLMReasoningEffort string
	LLMTimeout         time.Duration
	GoplsMode          string
	GoplsPath          string
	TypeScriptLSPMode  string
	TypeScriptLSPPath  string
	TypeScriptSDKPath  string
	SwiftLSPMode       string
	SwiftLSPPath       string
	PythonLSPMode      string
	PythonLSPPath      string
	RustLSPMode        string
	RustLSPPath        string
	EnableEmbeddings   bool
	EmbeddingModel     string
	EmbeddingBaseURL   string
	EmbeddingsAPIKey   string
}

type FieldError struct {
	Field   FieldKey `json:"field"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
}

type Resolved struct {
	Values      Values
	Sources     map[FieldKey]Source
	Errors      []FieldError
	Credentials CredentialReferences
}

func (r Resolved) Source(key FieldKey) Source { return r.Sources[key] }

func (r Resolved) HasError(key FieldKey) bool {
	for _, issue := range r.Errors {
		if issue.Field == key {
			return true
		}
	}
	return false
}

func (r Resolved) BootstrapError() error {
	for _, issue := range r.Errors {
		if definition, ok := Definition(issue.Field); ok && definition.ApplyMode == ApplyRestart {
			return fmt.Errorf("%s: %s", issue.Field, issue.Message)
		}
	}
	return nil
}
