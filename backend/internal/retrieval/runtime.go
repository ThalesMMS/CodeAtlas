package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type EmbeddingState string

const (
	EmbeddingDisabled    EmbeddingState = "disabled"
	EmbeddingAvailable   EmbeddingState = "available"
	EmbeddingRebuilding  EmbeddingState = "rebuilding"
	EmbeddingUnavailable EmbeddingState = "unavailable"
)

type EmbeddingFingerprint struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	ConfigurationHash string `json:"configurationHash"`
	Dimension         int    `json:"dimension"`
	TemplateVersion   string `json:"templateVersion"`
	Distance          string `json:"distance"`
}

func (f EmbeddingFingerprint) Metadata() domain.EmbeddingIndexMetadata {
	if f == (EmbeddingFingerprint{}) {
		return domain.EmbeddingIndexMetadata{}
	}
	return DesiredMetadata(f.Provider, f.Model, f.Dimension)
}

type EmbeddingConfiguration struct {
	Provider             ai.Provider
	Enabled              bool
	Model                string
	BaseURL              string
	CredentialGeneration string
}

type EmbeddingSnapshot struct {
	State       EmbeddingState       `json:"state"`
	Fingerprint EmbeddingFingerprint `json:"fingerprint"`
	provider    ai.Provider
}

type EmbeddingRuntime struct {
	current atomic.Pointer[EmbeddingSnapshot]
}

func NewEmbeddingRuntime(provider ai.Provider, enabled bool) *EmbeddingRuntime {
	provider = normalizedEmbeddingProvider(provider)
	state := EmbeddingDisabled
	if enabled {
		// Compatibility constructors historically treated an enabled provider as
		// immediately usable. Production settings use Prepare before activation.
		state = EmbeddingAvailable
	}
	runtime := &EmbeddingRuntime{}
	runtime.current.Store(&EmbeddingSnapshot{State: state, provider: provider})
	return runtime
}

func (r *EmbeddingRuntime) Snapshot() EmbeddingSnapshot {
	snapshot := r.load()
	return EmbeddingSnapshot{State: snapshot.State, Fingerprint: snapshot.Fingerprint}
}

func (r *EmbeddingRuntime) load() *EmbeddingSnapshot {
	if snapshot := r.current.Load(); snapshot != nil {
		return snapshot
	}
	return &EmbeddingSnapshot{State: EmbeddingDisabled, provider: ai.Disabled{}}
}

func (r *EmbeddingRuntime) Prepare(ctx context.Context, configuration EmbeddingConfiguration, stored domain.EmbeddingIndexMetadata, embeddingCount int) (*PreparedEmbedding, error) {
	provider := configuration.Provider
	if provider == nil {
		provider = r.load().provider
	}
	provider = normalizedEmbeddingProvider(provider)
	if !configuration.Enabled {
		return &PreparedEmbedding{runtime: r, candidate: &EmbeddingSnapshot{State: EmbeddingDisabled, provider: provider}}, nil
	}
	if strings.TrimSpace(configuration.Model) == "" {
		return nil, errors.New("embedding model is required")
	}
	if !provider.Available() {
		return nil, ai.ErrUnavailable
	}
	vectors, err := provider.Embed(ctx, []string{"codeatlas embedding dimension probe"})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, errors.New("invalid embedding probe response")
	}
	fingerprint := newEmbeddingFingerprint(configuration, len(vectors[0]))
	state := EmbeddingRebuilding
	needsRebuild := true
	if MetadataCompatible(stored, fingerprint.Metadata()) && embeddingCount > 0 {
		state = EmbeddingAvailable
		needsRebuild = false
	}
	return &PreparedEmbedding{
		runtime: r, candidate: &EmbeddingSnapshot{State: state, Fingerprint: fingerprint, provider: provider},
		needsRebuild: needsRebuild,
	}, nil
}

func (r *EmbeddingRuntime) MarkAvailable(fingerprint EmbeddingFingerprint) bool {
	for {
		current := r.load()
		if current.Fingerprint != fingerprint || current.State == EmbeddingDisabled {
			return false
		}
		next := &EmbeddingSnapshot{State: EmbeddingAvailable, Fingerprint: current.Fingerprint, provider: current.provider}
		if r.current.CompareAndSwap(current, next) {
			return true
		}
	}
}

func (r *EmbeddingRuntime) MarkFailed(fingerprint EmbeddingFingerprint) bool {
	for {
		current := r.load()
		if current.Fingerprint != fingerprint || current.State == EmbeddingDisabled {
			return false
		}
		next := &EmbeddingSnapshot{State: EmbeddingUnavailable, Fingerprint: current.Fingerprint, provider: current.provider}
		if r.current.CompareAndSwap(current, next) {
			return true
		}
	}
}

func (r *EmbeddingRuntime) forceAvailableForCompatibility() {
	for {
		current := r.load()
		next := &EmbeddingSnapshot{State: EmbeddingAvailable, Fingerprint: current.Fingerprint, provider: current.provider}
		if r.current.CompareAndSwap(current, next) {
			return
		}
	}
}

type PreparedEmbedding struct {
	runtime      *EmbeddingRuntime
	candidate    *EmbeddingSnapshot
	needsRebuild bool
	activate     sync.Once
}

func (p *PreparedEmbedding) Activate() {
	if p == nil {
		return
	}
	p.activate.Do(func() { p.runtime.current.Store(p.candidate) })
}

func (p *PreparedEmbedding) Abort(context.Context) {}

func (p *PreparedEmbedding) NeedsRebuild() bool { return p != nil && p.needsRebuild }

func (p *PreparedEmbedding) Fingerprint() EmbeddingFingerprint {
	if p == nil || p.candidate == nil {
		return EmbeddingFingerprint{}
	}
	return p.candidate.Fingerprint
}

func (p *PreparedEmbedding) State() EmbeddingState {
	if p == nil || p.candidate == nil {
		return EmbeddingDisabled
	}
	return p.candidate.State
}

func newEmbeddingFingerprint(configuration EmbeddingConfiguration, dimension int) EmbeddingFingerprint {
	normalizedEndpoint := normalizeEmbeddingEndpoint(configuration.BaseURL)
	sum := sha256.Sum256([]byte(normalizedEndpoint + "\x00" + configuration.CredentialGeneration))
	configurationHash := hex.EncodeToString(sum[:])
	return EmbeddingFingerprint{
		Provider:          "openai-compatible:embeddings:" + configurationHash[:16],
		Model:             strings.TrimSpace(configuration.Model),
		ConfigurationHash: configurationHash,
		Dimension:         dimension,
		TemplateVersion:   EmbeddingTemplateVersion,
		Distance:          EmbeddingDistance,
	}
}

func normalizeEmbeddingEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return endpoint
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String()
}

func normalizedEmbeddingProvider(provider ai.Provider) ai.Provider {
	if provider == nil {
		return ai.Disabled{}
	}
	return provider
}
