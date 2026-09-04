package settings

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func Resolve(environment Environment, overrides Overrides, credentials SecretValues) Resolved {
	resolved := Resolved{Sources: make(map[FieldKey]Source, len(documentedFields))}
	raw := make(map[FieldKey]any, len(documentedFields))
	for _, definition := range documentedFields {
		if definition.Secret {
			if value, ok := credentials[definition.Key]; ok {
				raw[definition.Key], resolved.Sources[definition.Key] = value, SourceSettings
			} else if value, ok := environment[definition.Key]; ok && value != "" {
				raw[definition.Key], resolved.Sources[definition.Key] = value, SourceEnv
			} else {
				raw[definition.Key], resolved.Sources[definition.Key] = "", SourceNone
			}
			continue
		}
		if value, ok := overrides[definition.Key]; ok {
			raw[definition.Key], resolved.Sources[definition.Key] = value, SourceSettings
		} else if value, ok := environment[definition.Key]; ok && strings.TrimSpace(value) != "" {
			raw[definition.Key], resolved.Sources[definition.Key] = value, SourceEnv
		} else {
			raw[definition.Key], resolved.Sources[definition.Key] = definition.Default, SourceDefault
		}
	}

	stringValue := func(key FieldKey) string {
		value, ok := raw[key].(string)
		if !ok {
			resolved.addError(key, "SETTINGS_TYPE_INVALID", "value must be a string")
			return ""
		}
		return strings.TrimSpace(value)
	}
	integerValue := func(key FieldKey) int64 {
		switch value := raw[key].(type) {
		case int64:
			return value
		case int:
			return int64(value)
		case float64:
			if value == float64(int64(value)) {
				return int64(value)
			}
		case string:
			parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil {
				return parsed
			}
		}
		resolved.addError(key, "SETTINGS_VALUE_INVALID", "value must be an integer")
		return 0
	}
	durationValue := func(key FieldKey) time.Duration {
		value := stringValue(key)
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			resolved.addError(key, "SETTINGS_VALUE_INVALID", "value must be a positive duration")
			return 0
		}
		return parsed
	}

	values := Values{
		Workspace: stringValue(FieldWorkspace), ListenAddress: stringValue(FieldListen), MaxFileBytes: integerValue(FieldMaxFileBytes),
		LLMBaseURL: strings.TrimRight(stringValue(FieldLLMBaseURL), "/"), LLMAPIKey: stringValue(FieldLLMAPIKey),
		LLMModel: stringValue(FieldLLMModel), LLMReasoningEffort: strings.ToLower(stringValue(FieldLLMReasoningEffort)), LLMTimeout: durationValue(FieldLLMTimeout),
		GoplsMode: strings.ToLower(stringValue(FieldGoplsMode)), GoplsPath: stringValue(FieldGoplsPath),
		TypeScriptLSPMode: strings.ToLower(stringValue(FieldTypeScriptLSPMode)), TypeScriptLSPPath: stringValue(FieldTypeScriptLSPPath), TypeScriptSDKPath: stringValue(FieldTypeScriptSDKPath),
		SwiftLSPMode: strings.ToLower(stringValue(FieldSwiftLSPMode)), SwiftLSPPath: stringValue(FieldSwiftLSPPath),
		PythonLSPMode: strings.ToLower(stringValue(FieldPythonLSPMode)), PythonLSPPath: stringValue(FieldPythonLSPPath),
		RustLSPMode: strings.ToLower(stringValue(FieldRustLSPMode)), RustLSPPath: stringValue(FieldRustLSPPath),
	}
	resolved.Values = values
	resolved.validate()
	return resolved
}

func EnvironmentFromLookup(lookup func(string) (string, bool)) Environment {
	result := make(Environment, len(documentedFields))
	for _, definition := range documentedFields {
		if value, ok := lookup(string(definition.Key)); ok {
			result[definition.Key] = value
		}
	}
	return result
}

func (r *Resolved) validate() {
	if r.Values.Workspace == "" {
		r.addError(FieldWorkspace, "SETTINGS_VALUE_INVALID", "workspace cannot be empty")
	}
	if _, _, err := net.SplitHostPort(r.Values.ListenAddress); err != nil {
		r.addError(FieldListen, "SETTINGS_VALUE_INVALID", "listen address must be host:port")
	}
	if r.Values.MaxFileBytes <= 0 {
		r.addError(FieldMaxFileBytes, "SETTINGS_VALUE_INVALID", "maximum file size must be positive")
	}
	if r.Values.LLMBaseURL == "" {
		r.addError(FieldLLMBaseURL, "PROVIDER_URL_REQUIRED", "LLM base URL is required")
	} else if !validHTTPURL(r.Values.LLMBaseURL) {
		r.addError(FieldLLMBaseURL, "PROVIDER_URL_INVALID", "LLM base URL must be an http(s) URL")
	}
	if r.Values.LLMModel == "" {
		r.addError(FieldLLMModel, "CHAT_MODEL_REQUIRED", "LLM model is required")
	}
	switch r.Values.LLMReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		r.addError(FieldLLMReasoningEffort, "SETTINGS_VALUE_INVALID", "unsupported reasoning effort")
	}
	if r.Values.LLMTimeout <= 0 {
		r.addError(FieldLLMTimeout, "SETTINGS_VALUE_INVALID", "LLM timeout must be positive")
	}
	r.validateLSP(FieldGoplsMode, FieldGoplsPath, r.Values.GoplsMode, r.Values.GoplsPath)
	r.validateLSP(FieldTypeScriptLSPMode, FieldTypeScriptLSPPath, r.Values.TypeScriptLSPMode, r.Values.TypeScriptLSPPath)
	r.validateLSP(FieldSwiftLSPMode, FieldSwiftLSPPath, r.Values.SwiftLSPMode, r.Values.SwiftLSPPath)
	r.validateLSP(FieldPythonLSPMode, FieldPythonLSPPath, r.Values.PythonLSPMode, r.Values.PythonLSPPath)
	r.validateLSP(FieldRustLSPMode, FieldRustLSPPath, r.Values.RustLSPMode, r.Values.RustLSPPath)
}

func (r *Resolved) validateLSP(modeKey, pathKey FieldKey, mode, path string) {
	switch mode {
	case "auto", "true", "false":
	default:
		r.addError(modeKey, "SETTINGS_VALUE_INVALID", "language-server mode must be auto, true, or false")
	}
	if path == "" {
		r.addError(pathKey, "SETTINGS_VALUE_INVALID", "language-server path cannot be empty")
	}
}

func (r *Resolved) addError(key FieldKey, code, message string) {
	for _, existing := range r.Errors {
		if existing.Field == key {
			return
		}
	}
	r.Errors = append(r.Errors, FieldError{Field: key, Code: code, Message: message})
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
