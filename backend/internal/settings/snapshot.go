package settings

import (
	"reflect"
)

type FieldSnapshot struct {
	Key          FieldKey  `json:"key"`
	Kind         ValueKind `json:"kind"`
	Value        any       `json:"value,omitempty"`
	RunningValue any       `json:"runningValue,omitempty"`
	Source       Source    `json:"source"`
	ApplyMode    ApplyMode `json:"applyMode"`
	AllowEmpty   bool      `json:"allowEmpty,omitempty"`
	Configured   *bool     `json:"configured,omitempty"`
}

type SanitizedSnapshot struct {
	Revision        uint64                    `json:"revision"`
	Groups          map[Group][]FieldSnapshot `json:"groups"`
	RestartRequired []FieldKey                `json:"restartRequired"`
	Validation      []FieldError              `json:"validation,omitempty"`
}

func makeSnapshot(revision uint64, resolved Resolved, running Values) SanitizedSnapshot {
	snapshot := SanitizedSnapshot{
		Revision:   revision,
		Groups:     make(map[Group][]FieldSnapshot, 4),
		Validation: append([]FieldError(nil), resolved.Errors...),
	}
	for _, definition := range DocumentedFields() {
		field := FieldSnapshot{
			Key:        definition.Key,
			Kind:       definition.Kind,
			Source:     resolved.Source(definition.Key),
			ApplyMode:  definition.ApplyMode,
			AllowEmpty: definition.AllowEmpty,
		}
		value := valueForField(resolved.Values, definition.Key)
		if definition.Secret {
			configured := value != ""
			field.Configured = &configured
		} else {
			field.Value = value
			if definition.ApplyMode == ApplyRestart {
				field.RunningValue = valueForField(running, definition.Key)
				if !reflect.DeepEqual(field.Value, field.RunningValue) {
					snapshot.RestartRequired = append(snapshot.RestartRequired, definition.Key)
				}
			}
		}
		snapshot.Groups[definition.Group] = append(snapshot.Groups[definition.Group], field)
	}
	return snapshot
}

func cloneSnapshot(snapshot SanitizedSnapshot) SanitizedSnapshot {
	cloned := SanitizedSnapshot{
		Revision:        snapshot.Revision,
		Groups:          make(map[Group][]FieldSnapshot, len(snapshot.Groups)),
		RestartRequired: append([]FieldKey(nil), snapshot.RestartRequired...),
		Validation:      append([]FieldError(nil), snapshot.Validation...),
	}
	for group, fields := range snapshot.Groups {
		clonedFields := append([]FieldSnapshot(nil), fields...)
		for index := range clonedFields {
			if fields[index].Configured != nil {
				configured := *fields[index].Configured
				clonedFields[index].Configured = &configured
			}
		}
		cloned.Groups[group] = clonedFields
	}
	return cloned
}

func valueForField(values Values, key FieldKey) any {
	switch key {
	case FieldWorkspace:
		return values.Workspace
	case FieldListen:
		return values.ListenAddress
	case FieldMaxFileBytes:
		return values.MaxFileBytes
	case FieldLLMBaseURL:
		return values.LLMBaseURL
	case FieldLLMAPIKey:
		return values.LLMAPIKey
	case FieldLLMModel:
		return values.LLMModel
	case FieldLLMReasoningEffort:
		return values.LLMReasoningEffort
	case FieldLLMTimeout:
		return values.LLMTimeout.String()
	case FieldGoplsMode:
		return values.GoplsMode
	case FieldGoplsPath:
		return values.GoplsPath
	case FieldTypeScriptLSPMode:
		return values.TypeScriptLSPMode
	case FieldTypeScriptLSPPath:
		return values.TypeScriptLSPPath
	case FieldTypeScriptSDKPath:
		return values.TypeScriptSDKPath
	case FieldSwiftLSPMode:
		return values.SwiftLSPMode
	case FieldSwiftLSPPath:
		return values.SwiftLSPPath
	case FieldPythonLSPMode:
		return values.PythonLSPMode
	case FieldPythonLSPPath:
		return values.PythonLSPPath
	case FieldRustLSPMode:
		return values.RustLSPMode
	case FieldRustLSPPath:
		return values.RustLSPPath
	case FieldEnableEmbeddings:
		return values.EnableEmbeddings
	case FieldEmbeddingModel:
		return values.EmbeddingModel
	case FieldEmbeddingBaseURL:
		return values.EmbeddingBaseURL
	case FieldEmbeddingsAPIKey:
		return values.EmbeddingsAPIKey
	default:
		return nil
	}
}
