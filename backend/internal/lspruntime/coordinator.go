package lspruntime

import (
	"context"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/semantic"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

type Family string

const (
	FamilyGo         Family = "go"
	FamilyTypeScript Family = "typescript"
	FamilySwift      Family = "swift"
	FamilyPython     Family = "python"
	FamilyRust       Family = "rust"
)

var familyOrder = []Family{FamilyGo, FamilyTypeScript, FamilySwift, FamilyPython, FamilyRust}

type Slot struct {
	Provider   semantic.SemanticProvider
	Capability capabilities.Result
	Shutdown   func(context.Context) error
}

type Factory func(context.Context, settings.Values) (Slot, error)

type Factories struct {
	Go         Factory
	TypeScript Factory
	Swift      Factory
	Python     Factory
	Rust       Factory
}

func (f Factories) forFamily(family Family) Factory {
	switch family {
	case FamilyGo:
		return f.Go
	case FamilyTypeScript:
		return f.TypeScript
	case FamilySwift:
		return f.Swift
	case FamilyPython:
		return f.Python
	case FamilyRust:
		return f.Rust
	default:
		return nil
	}
}

type capabilitySink interface {
	UpdateCapability(capabilities.Result)
}

type Coordinator struct {
	mu              sync.Mutex
	runtime         *semantic.Runtime
	factories       Factories
	registry        capabilitySink
	slots           map[Family]Slot
	shutdownTimeout time.Duration
}

func NewCoordinator(runtime *semantic.Runtime, factories Factories, registry capabilitySink, initial map[Family]Slot, shutdownTimeout time.Duration) *Coordinator {
	if runtime == nil {
		runtime = semantic.NewRuntime()
	}
	if shutdownTimeout <= 0 {
		shutdownTimeout = 10 * time.Second
	}
	return &Coordinator{
		runtime: runtime, factories: factories, registry: registry,
		slots: cloneSlots(initial), shutdownTimeout: shutdownTimeout,
	}
}

// Prepared owns both the semantic replay lock and the coordinator write lock
// until Activate or Abort is called. Its closures are mutually exclusive and
// idempotent.
type Prepared struct {
	finish   sync.Once
	activate func()
	abort    func(context.Context)
}

func (p *Prepared) Activate() {
	if p == nil {
		return
	}
	p.finish.Do(p.activate)
}

func (p *Prepared) Abort(ctx context.Context) {
	if p == nil {
		return
	}
	p.finish.Do(func() { p.abort(ctx) })
}

func (c *Coordinator) Prepare(ctx context.Context, values settings.Values, changes settings.ChangeSet) (*Prepared, []settings.FieldError) {
	c.mu.Lock()
	candidateSlots := cloneSlots(c.slots)
	changed := make([]Family, 0, len(familyOrder))
	built := make([]Family, 0, len(familyOrder))

	for _, family := range familyOrder {
		if !familyChanged(changes, family) {
			continue
		}
		changed = append(changed, family)
		if familyMode(values, family) == "false" {
			candidateSlots[family] = disabledSlot(family)
			continue
		}
		factory := c.factories.forFamily(family)
		if factory == nil {
			c.shutdownFamilies(context.Background(), candidateSlots, built)
			c.mu.Unlock()
			return nil, []settings.FieldError{{Field: familyPathField(family), Code: "LSP_FACTORY_UNAVAILABLE", Message: "language-server factory is unavailable"}}
		}
		slot, err := factory(ctx, values)
		if err != nil {
			c.shutdownFamilies(context.Background(), candidateSlots, built)
			c.mu.Unlock()
			return nil, []settings.FieldError{{Field: familyPathField(family), Code: "LSP_CANDIDATE_FAILED", Message: "language-server candidate could not start"}}
		}
		slot.Capability = sanitizeCapability(family, slot.Capability)
		candidateSlots[family] = slot
		built = append(built, family)
	}

	candidateRouter := routerForSlots(candidateSlots)
	activateRouter, abortRouter, err := c.runtime.Prepare(ctx, candidateRouter)
	if err != nil {
		c.shutdownFamilies(context.Background(), candidateSlots, built)
		c.mu.Unlock()
		field := settings.FieldGoplsPath
		if len(changed) > 0 {
			field = familyPathField(changed[0])
		}
		return nil, []settings.FieldError{{Field: field, Code: "LSP_DOCUMENT_REPLAY_FAILED", Message: "open documents could not be prepared for the language server"}}
	}

	oldSlots := cloneSlots(c.slots)
	prepared := &Prepared{}
	prepared.activate = func() {
		activateRouter()
		c.slots = candidateSlots
		if c.registry != nil {
			for _, family := range changed {
				c.registry.UpdateCapability(candidateSlots[family].Capability)
			}
		}
		c.mu.Unlock()
		c.shutdownFamilies(context.Background(), oldSlots, changed)
	}
	prepared.abort = func(abortContext context.Context) {
		abortRouter(abortContext)
		c.mu.Unlock()
		c.shutdownFamilies(abortContext, candidateSlots, built)
	}
	return prepared, nil
}

func (c *Coordinator) Shutdown(ctx context.Context) {
	c.mu.Lock()
	slots := cloneSlots(c.slots)
	c.mu.Unlock()
	c.shutdownFamilies(ctx, slots, familyOrder)
}

func (c *Coordinator) shutdownFamilies(parent context.Context, slots map[Family]Slot, families []Family) {
	for _, family := range families {
		shutdown := slots[family].Shutdown
		if shutdown == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, c.shutdownTimeout)
		_ = shutdown(ctx)
		cancel()
	}
}

func cloneSlots(slots map[Family]Slot) map[Family]Slot {
	result := make(map[Family]Slot, len(slots))
	for family, slot := range slots {
		if slot.Capability.Metadata != nil {
			metadata := make(map[string]string, len(slot.Capability.Metadata))
			for key, value := range slot.Capability.Metadata {
				metadata[key] = value
			}
			slot.Capability.Metadata = metadata
		}
		result[family] = slot
	}
	return result
}

func familyChanged(changes settings.ChangeSet, family Family) bool {
	for _, key := range familyFields(family) {
		if changes.Changed(key) {
			return true
		}
	}
	return false
}

func familyFields(family Family) []settings.FieldKey {
	switch family {
	case FamilyGo:
		return []settings.FieldKey{settings.FieldGoplsMode, settings.FieldGoplsPath}
	case FamilyTypeScript:
		return []settings.FieldKey{settings.FieldTypeScriptLSPMode, settings.FieldTypeScriptLSPPath, settings.FieldTypeScriptSDKPath}
	case FamilySwift:
		return []settings.FieldKey{settings.FieldSwiftLSPMode, settings.FieldSwiftLSPPath}
	case FamilyPython:
		return []settings.FieldKey{settings.FieldPythonLSPMode, settings.FieldPythonLSPPath}
	case FamilyRust:
		return []settings.FieldKey{settings.FieldRustLSPMode, settings.FieldRustLSPPath}
	default:
		return nil
	}
}

func familyMode(values settings.Values, family Family) string {
	switch family {
	case FamilyGo:
		return values.GoplsMode
	case FamilyTypeScript:
		return values.TypeScriptLSPMode
	case FamilySwift:
		return values.SwiftLSPMode
	case FamilyPython:
		return values.PythonLSPMode
	case FamilyRust:
		return values.RustLSPMode
	default:
		return "false"
	}
}

func familyPathField(family Family) settings.FieldKey {
	switch family {
	case FamilyGo:
		return settings.FieldGoplsPath
	case FamilyTypeScript:
		return settings.FieldTypeScriptLSPPath
	case FamilySwift:
		return settings.FieldSwiftLSPPath
	case FamilyPython:
		return settings.FieldPythonLSPPath
	case FamilyRust:
		return settings.FieldRustLSPPath
	default:
		return settings.FieldGoplsPath
	}
}

func familyExtensions(family Family) []string {
	switch family {
	case FamilyGo:
		return []string{".go"}
	case FamilyTypeScript:
		return []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}
	case FamilySwift:
		return []string{".swift"}
	case FamilyPython:
		return []string{".py"}
	case FamilyRust:
		return []string{".rs"}
	default:
		return nil
	}
}

func routerForSlots(slots map[Family]Slot) *semantic.PathRouter {
	routes := make(map[string]semantic.SemanticProvider)
	for _, family := range familyOrder {
		provider := slots[family].Provider
		if provider == nil {
			continue
		}
		for _, extension := range familyExtensions(family) {
			routes[extension] = provider
		}
	}
	return semantic.NewPathRouter(routes)
}

func capabilityID(family Family) capabilities.CapabilityID {
	switch family {
	case FamilyGo:
		return capabilities.CapabilityGopls
	case FamilyTypeScript:
		return capabilities.CapabilityTypeScriptLSP
	case FamilySwift:
		return capabilities.CapabilitySourceKitLSP
	case FamilyPython:
		return capabilities.CapabilityPythonLSP
	case FamilyRust:
		return capabilities.CapabilityRustAnalyzer
	default:
		return capabilities.CapabilityID(family)
	}
}

func disabledSlot(family Family) Slot {
	return Slot{Capability: capabilities.Result{
		ID: capabilityID(family), Requirement: capabilities.Optional,
		State: capabilities.CapabilityDisabled, CheckedAt: time.Now().UTC(),
	}}
}

func sanitizeCapability(family Family, result capabilities.Result) capabilities.Result {
	result.ID = capabilityID(family)
	result.Requirement = capabilities.Optional
	result.Message = ""
	allowed := map[string]bool{
		"version": true, "positionEncoding": true, "languageFamily": true,
		"extensions": true, "semanticTokensFull": true, "documentSync": true,
		"projectMode": true,
	}
	metadata := make(map[string]string)
	for key, value := range result.Metadata {
		if allowed[key] {
			metadata[key] = value
		}
	}
	result.Metadata = metadata
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	return result
}
