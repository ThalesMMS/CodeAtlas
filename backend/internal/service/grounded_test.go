package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/aiout"
	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/contextpack"
)

// scriptedProvider returns a fixed sequence of responses (one per Complete call)
// and records the prompts it received, so a test can drive the controlled retry.
type scriptedProvider struct {
	responses []string
	errors    []error
	calls     int
	prompts   []string
}

type categorizedValidationError string

func (e categorizedValidationError) Error() string             { return "raw detail must not be reused" }
func (e categorizedValidationError) ValidationSummary() string { return string(e) }

func (p *scriptedProvider) Name() string    { return "scripted" }
func (p *scriptedProvider) Available() bool { return true }
func (p *scriptedProvider) Complete(_ context.Context, _ string, userPrompt string, _ int) (string, error) {
	p.prompts = append(p.prompts, userPrompt)
	index := p.calls
	p.calls++
	if index < len(p.errors) && p.errors[index] != nil {
		return "", p.errors[index]
	}
	if index >= len(p.responses) {
		return "", errors.New("no more scripted responses")
	}
	return p.responses[index], nil
}
func (p *scriptedProvider) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ai.ErrUnavailable
}

func TestGenerateGroundedPreservesContextErrors(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		cause := cause
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()
			provider := &scriptedProvider{responses: []string{""}, errors: []error{cause}}
			err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "task"}, func([]byte) error { return nil })
			if !errors.Is(err, cause) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, cause)
			}
			if appErr, ok := apperror.As(err); ok && appErr.Code == apperror.CodeProviderUnavailable {
				t.Fatalf("context error was wrapped as %s", appErr.Code)
			}
		})
	}
}

func TestGenerateGroundedPrefersCanceledRequestContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &scriptedProvider{responses: []string{""}, errors: []error{errors.New("transport stopped")}}
	err := generateGrounded(ctx, provider, ai.GenerationRequest{UserPrompt: "task"}, func([]byte) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled request context", err)
	}
}

func TestGenerateGroundedPreservesContextErrorFromCorrectionAttempt(t *testing.T) {
	t.Parallel()
	invalid := `{"schemaVersion":"explanation/v2","summary":"x","observations":[{"text":"y","evidenceIds":["ev:missing"]}],"inferences":[],"uncertainties":[],"changeImpact":[]}`
	provider := &scriptedProvider{
		responses: []string{invalid, ""},
		errors:    []error{nil, context.DeadlineExceeded},
	}
	err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "task"}, explanationValidate(aiout.AllowSet([]string{"ev:1"})))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("correction error = %v, want deadline exceeded", err)
	}
}

func explanationValidate(allow map[string]struct{}) func([]byte) error {
	return func(raw []byte) error {
		var out aiout.Explanation
		if err := aiout.DecodeStrict(raw, &out); err != nil {
			return err
		}
		return aiout.ValidateExplanation(allow, out, false)
	}
}

func TestGenerateGroundedRetriesOnceThenSucceeds(t *testing.T) {
	t.Parallel()
	allow := aiout.AllowSet([]string{"ev:1"})
	// The model's prose carries a distinctive marker; the rejected evidence ID is a
	// short identifier. The retry may name the offending ID (correction feedback)
	// but must never echo the model's free text back into the prompt.
	invalid := `{"schemaVersion":"explanation/v2","summary":"LEAKED_PROSE_MARKER","observations":[{"text":"LEAKED_PROSE_MARKER","evidenceIds":["ev:BAD"]}],"inferences":[],"uncertainties":[],"changeImpact":[]}`
	valid := `{"schemaVersion":"explanation/v2","summary":"x","observations":[],"inferences":[],"uncertainties":[],"changeImpact":[]}`
	provider := &scriptedProvider{responses: []string{invalid, valid}}

	err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "task"}, explanationValidate(allow))
	if err != nil {
		t.Fatalf("expected success after one retry, got %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("expected exactly 2 calls (initial + one retry), got %d", provider.calls)
	}
	if !strings.Contains(provider.prompts[1], "task") {
		t.Fatal("retry dropped the original task")
	}
	if strings.Contains(provider.prompts[1], "LEAKED_PROSE_MARKER") {
		t.Fatal("retry leaked the model's raw free text into the prompt")
	}
}

func TestGenerateGroundedFailsClosedAfterRetry(t *testing.T) {
	t.Parallel()
	allow := aiout.AllowSet([]string{"ev:1"})
	invalid := `{"schemaVersion":"explanation/v2","summary":"x","observations":[{"text":"y","evidenceIds":["ev:INVENTADO"]}],"inferences":[],"uncertainties":[],"changeImpact":[]}`
	provider := &scriptedProvider{responses: []string{invalid, invalid}}

	err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "task"}, explanationValidate(allow))
	appErr, ok := apperror.As(err)
	if !ok || appErr.Code != apperror.CodeModelOutputInvalid {
		t.Fatalf("error = %v, want MODEL_OUTPUT_INVALID", err)
	}
	if provider.calls != 2 {
		t.Fatalf("expected at most 2 calls (initial + one retry), got %d", provider.calls)
	}
	if appErr.Cause == nil || !strings.Contains(appErr.Cause.Error(), "references unknown evidence ID") {
		t.Fatalf("MODEL_OUTPUT_INVALID cause = %v, want sanitized validation reason", appErr.Cause)
	}
	// The diagnostic must preserve the violated rule without carrying the raw
	// model-authored identifier into logs.
	if strings.Contains(appErr.Cause.Error(), "ev:INVENTADO") {
		t.Fatal("MODEL_OUTPUT_INVALID leaked raw rejected content")
	}
}

func TestGenerateGroundedWithAttemptsCanRecoverOnThirdResponse(t *testing.T) {
	t.Parallel()
	allow := aiout.AllowSet([]string{"ev:1"})
	invalid := `{"schemaVersion":"explanation/v2","summary":"RAW_REJECTED_MARKER","observations":[{"text":"x","evidenceIds":["ev:missing"]}],"inferences":[],"uncertainties":[],"changeImpact":[]}`
	valid := `{"schemaVersion":"explanation/v2","summary":"ok","observations":[],"inferences":[],"uncertainties":[],"changeImpact":[]}`
	provider := &scriptedProvider{responses: []string{invalid, invalid, valid}}

	err := generateGroundedWithAttempts(context.Background(), provider, ai.GenerationRequest{UserPrompt: "task"}, 3, explanationValidate(allow))
	if err != nil {
		t.Fatalf("expected success on the third bounded attempt, got %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d, want 3", provider.calls)
	}
	for index, prompt := range provider.prompts[1:] {
		if !strings.Contains(prompt, "references unknown evidence ID") {
			t.Fatalf("correction prompt %d omitted the validation rule: %q", index+1, prompt)
		}
		if strings.Contains(prompt, "RAW_REJECTED_MARKER") {
			t.Fatalf("correction prompt %d leaked rejected model text", index+1)
		}
	}
}

func TestGenerateGroundedUsesContentFreeValidationCategory(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{responses: []string{`{}`, `{}`}}
	err := generateGrounded(context.Background(), provider, ai.GenerationRequest{
		Operation: "deepwiki-plan", UserPrompt: "inventory",
	}, func([]byte) error {
		return categorizedValidationError("unknown scope path")
	})
	if err == nil {
		t.Fatal("generateGrounded succeeded after two invalid plans")
	}
	if len(provider.prompts) != 2 || !strings.Contains(provider.prompts[1], "unknown scope path") {
		t.Fatalf("repair prompt = %#v, want content-free category", provider.prompts)
	}
	if strings.Contains(provider.prompts[1], "raw detail") {
		t.Fatalf("repair prompt leaked Error() detail: %q", provider.prompts[1])
	}
}

func TestValidationDiagnosticSummaryRedactsAndBoundsModelValues(t *testing.T) {
	t.Parallel()
	marker := "RAW_MODEL_MARKER_" + strings.Repeat("x", 2000)
	summary := validationDiagnosticSummary(&aiout.ValidationError{Problems: []string{
		`sections[0].claims[0] references unknown evidence ID "` + marker + `"`,
	}})
	if strings.Contains(summary, marker) {
		t.Fatalf("diagnostic leaked model value: %q", summary)
	}
	if !strings.Contains(summary, "references unknown evidence ID") || !strings.Contains(summary, "<redacted>") {
		t.Fatalf("diagnostic lost sanitized rule: %q", summary)
	}
	if len([]rune(summary)) > 1201 {
		t.Fatalf("diagnostic has %d runes, want bounded output", len([]rune(summary)))
	}
}

func TestGenerateGroundedRetriesUnknownCodeEvidenceID(t *testing.T) {
	t.Parallel()
	allow := aiout.AllowSet([]string{"ev:1"})
	invalid := `{"schemaVersion":"explanation/v2","summary":"x","codeEvidenceIds":["ev:missing"],"observations":[],"inferences":[],"uncertainties":[],"changeImpact":[{"text":"impact","evidenceIds":["ev:1"]}]}`
	valid := `{"schemaVersion":"explanation/v2","summary":"x","codeEvidenceIds":["ev:1"],"observations":[],"inferences":[],"uncertainties":[],"changeImpact":[{"text":"impact","evidenceIds":["ev:1"]}]}`
	provider := &scriptedProvider{responses: []string{invalid, valid}}
	validate := func(raw []byte) error {
		var out aiout.Explanation
		if err := aiout.DecodeStrict(raw, &out); err != nil {
			return err
		}
		return aiout.ValidateExplanation(allow, out, true)
	}
	if err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "task"}, validate); err != nil {
		t.Fatalf("expected corrected code EvidenceID to pass retry: %v", err)
	}
	if provider.calls != 2 || !strings.Contains(provider.prompts[1], "codeEvidenceIds") {
		t.Fatalf("controlled retry was not triggered for code EvidenceID: calls=%d prompts=%v", provider.calls, provider.prompts)
	}
}

func TestGenerateGroundedRetriesInvalidCodemapFlowNodeID(t *testing.T) {
	t.Parallel()
	invalid := `{"schemaVersion":"codemap-narrative/v2","title":"Map","overview":"Flow","trace":[],"flows":[{"title":"Request","entryNodeId":"node:entry","steps":[{"label":"1a","nodeId":"node:invented","text":"Start"}]}],"claims":[],"inferences":[],"uncertainties":[]}`
	valid := `{"schemaVersion":"codemap-narrative/v2","title":"Map","overview":"Flow","trace":[],"flows":[{"title":"Request","entryNodeId":"node:entry","steps":[{"label":"1a","nodeId":"node:entry","text":"Start"}]}],"claims":[],"inferences":[],"uncertainties":[]}`
	provider := &scriptedProvider{responses: []string{invalid, valid}}
	validate := func(raw []byte) error {
		var out aiout.CodemapNarrative
		if err := aiout.DecodeStrict(raw, &out); err != nil {
			return err
		}
		allowedNodes := aiout.AllowSet([]string{"node:entry"})
		return aiout.ValidateCodemap(allowedNodes, allowedNodes, allowedNodes, out)
	}
	if err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "codemap"}, validate); err != nil {
		t.Fatalf("expected corrected flow node ID to pass retry: %v", err)
	}
	if provider.calls != 2 || !strings.Contains(provider.prompts[1], "flows[0].steps[0].nodeId") {
		t.Fatalf("controlled retry was not triggered for flow node ID: calls=%d prompts=%v", provider.calls, provider.prompts)
	}
}

func TestValidateCodeSelectionsRejectsAllowedButNonRenderableEvidence(t *testing.T) {
	t.Parallel()
	pack := contextpack.ContextPack{Evidence: []contextpack.Evidence{
		{ID: "ev:code", Path: "main.go", DisplayCode: "func main() {}"},
		{ID: "ev:lsp", Path: "main.go", Content: "hover text"},
	}}
	if err := validateCodeSelections(pack, []string{"ev:code"}); err != nil {
		t.Fatalf("renderable code rejected: %v", err)
	}
	if err := validateCodeSelections(pack, []string{"ev:lsp"}); err == nil || !strings.Contains(err.Error(), "non-renderable") {
		t.Fatalf("non-renderable selection error = %v", err)
	}
}

func TestPackRelevantFilesDeduplicatesPaths(t *testing.T) {
	t.Parallel()
	pack := contextpack.ContextPack{Evidence: []contextpack.Evidence{
		{Path: "pkg/service.go"}, {Path: "pkg/service.go"}, {Path: "pkg/model.go"},
	}}
	refs := packRelevantFiles(pack)
	if len(refs) != 2 || refs[0].Path != "pkg/service.go" || refs[1].Path != "pkg/model.go" {
		t.Fatalf("relevant files = %+v, want first occurrence of two unique paths", refs)
	}
}

func TestGenerateGroundedRejectsInjectedHTMLAndUnknownFields(t *testing.T) {
	t.Parallel()
	allow := aiout.AllowSet([]string{"ev:1"})
	// A response with an unknown field (e.g. an injected "path") is rejected by the
	// strict decoder; one retry then fails closed.
	injected := `{"schemaVersion":"explanation/v2","summary":"x","observations":[],"inferences":[],"uncertainties":[],"changeImpact":[],"path":"/etc/passwd"}`
	provider := &scriptedProvider{responses: []string{injected, injected}}
	err := generateGrounded(context.Background(), provider, ai.GenerationRequest{UserPrompt: "t"}, explanationValidate(allow))
	if appErr, ok := apperror.As(err); !ok || appErr.Code != apperror.CodeModelOutputInvalid {
		t.Fatalf("error = %v, want MODEL_OUTPUT_INVALID for injected field", err)
	}
}
