package contextpack

import (
	"context"

	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
)

// SemanticRequest asks a SemanticSource for facts over the SAME pinned view the
// packer is using. The source never opens its own view.
type SemanticRequest struct {
	View            repository.ReadView
	Feature         Feature
	Location        *SourceLocation
	Methods         []string // the LSP methods the policy wants (hover, definition, …)
	Mandatory       bool     // when true, unavailability fails the pack
	DocumentID      semantic.DocumentID
	DocumentVersion semantic.DocumentVersion
	Content         []byte
	Preloaded       map[string]SemanticOutcome
}

// SemanticResult is the source's contribution: extra evidence plus omissions that
// explain unavailability/limits. Unavailability is never a silent empty slice.
type SemanticResult struct {
	Evidence  []Evidence
	Omissions []Omission
}

// SemanticSource collects semantic (e.g. gopls) evidence for a request. It applies
// its own deadline/fan-out and returns an error only for a mandatory-capability
// failure; disabled/optional unavailability is reported via omissions.
type SemanticSource interface {
	Collect(ctx context.Context, request SemanticRequest) (SemanticResult, error)
}

// SemanticPolicy is the optional capability of a policy that wants semantic
// evidence: it declares which methods to request and whether the source is
// mandatory for this feature.
type SemanticPolicy interface {
	SemanticMethods(request ContextRequest) (methods []string, mandatory bool)
}

// SemanticPriorityPolicy lets a feature place high-confidence semantic facts
// before deterministic evidence in the serialized pack. Policies that do not
// implement it retain the default AST-first ordering.
type SemanticPriorityPolicy interface {
	SemanticEvidenceFirst() bool
}

// WithSemanticSource sets the optional semantic evidence source. When set, a
// policy that implements SemanticPolicy gets its requested facts merged into the
// pack (as candidates) with their provenance, and the source's omissions recorded.
func (p *Packer) WithSemanticSource(source SemanticSource) *Packer {
	p.semantic = source
	return p
}
