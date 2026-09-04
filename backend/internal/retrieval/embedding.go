package retrieval

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// EmbeddingTemplateVersion identifies the embedding text template. Any
// incompatible change to SymbolEmbeddingTextV2 must bump this version so old
// vectors are invalidated.
const EmbeddingTemplateVersion = "v2"

// EmbeddingDistance is the similarity metric the dense index is built for.
const EmbeddingDistance = "cosine"

// SymbolEmbeddingTextV2 builds the vectorized text from stable, bounded fields,
// preferring declared intent before the synthetic structural summary.
func SymbolEmbeddingTextV2(symbol domain.Symbol) string {
	return strings.Join([]string{
		symbol.Language,
		symbol.Kind,
		symbol.QualifiedName,
		symbol.Signature,
		symbol.DocComment,
		symbol.Summary,
		textutil.CompactCode(symbol.Code, 4_000),
	}, "\n")
}

// DesiredMetadata is the embedding configuration this process expects.
func DesiredMetadata(providerID, model string, dimension int) domain.EmbeddingIndexMetadata {
	return domain.EmbeddingIndexMetadata{
		Enabled:         true,
		Provider:        providerID,
		Model:           model,
		Dimension:       dimension,
		TemplateVersion: EmbeddingTemplateVersion,
		Distance:        EmbeddingDistance,
	}
}

// MetadataCompatible reports whether a stored configuration matches the desired
// one (ignoring the build timestamp). A legacy snapshot with absent metadata is
// never compatible.
func MetadataCompatible(stored, desired domain.EmbeddingIndexMetadata) bool {
	return stored.Enabled == desired.Enabled &&
		stored.Provider == desired.Provider &&
		stored.Model == desired.Model &&
		stored.Dimension == desired.Dimension &&
		stored.TemplateVersion == desired.TemplateVersion &&
		stored.Distance == desired.Distance
}

// validateIncrementalVectors guards the incremental refresh path so a single
// scan can never mix incompatible vectors into the published index. Every vector
// must be non-empty, finite and — once the index dimension is established — match
// it exactly. A dimension of zero means the index has no metadata yet (first
// indexing before reconciliation), so only finiteness is enforced.
func validateIncrementalVectors(vectors map[string][]float64, dimension int) error {
	for id, vector := range vectors {
		if len(vector) == 0 {
			return fmt.Errorf("empty embedding for %s", id)
		}
		if dimension > 0 && len(vector) != dimension {
			return fmt.Errorf("embedding dimension %d != index dimension %d for %s", len(vector), dimension, id)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("non-finite embedding value for %s", id)
			}
		}
	}
	return nil
}

// EmbeddingProgress reports rebuild progress as symbols are embedded. It is
// called after every provider batch with the number of symbols done so far and
// the total scheduled for this rebuild.
type EmbeddingProgress func(completed, total int)

// PrepareStartup decides how the dense index is used for this process without
// generating any vectors. When embeddings are disabled it is a no-op. Otherwise
// it probes the provider dimension and compares the persisted metadata: a
// compatible, non-empty index becomes available immediately; anything else
// leaves the runtime in the rebuilding state so index scans skip embeddings and
// the caller schedules a background rebuild (see RebuildEmbeddingsWithProgress).
// It returns whether that rebuild is required.
func (h *Hybrid) PrepareStartup(ctx context.Context, providerID, model string) (bool, error) {
	embeddingSnapshot := h.embeddings.load()
	if embeddingSnapshot.State == EmbeddingDisabled {
		return false, nil
	}
	if !embeddingSnapshot.provider.Available() {
		return false, ai.ErrUnavailable
	}
	dimension, err := h.probeDimension(ctx, embeddingSnapshot.provider)
	if err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return false, err
	}
	desired := DesiredMetadata(providerID, model, dimension)
	// A fingerprint prepared by live settings activation carries the credential
	// generation; keep it only while it still describes this configuration.
	fingerprint := embeddingSnapshot.Fingerprint
	if fingerprint == (EmbeddingFingerprint{}) || !MetadataCompatible(fingerprint.Metadata(), desired) {
		fingerprint = fingerprintFromMetadata(desired)
	}
	metadata, err := h.store.EmbeddingMetadataContext(ctx)
	if err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return false, err
	}
	if MetadataCompatible(metadata, desired) && h.store.EmbeddingCount() > 0 {
		h.embeddings.setStartupState(EmbeddingAvailable, fingerprint)
		return false, nil
	}
	h.embeddings.setStartupState(EmbeddingRebuilding, fingerprint)
	return true, nil
}

// EmbeddingSnapshot exposes the current dense-index runtime state.
func (h *Hybrid) EmbeddingSnapshot() EmbeddingSnapshot {
	return h.embeddings.Snapshot()
}

// fingerprintFromMetadata derives the runtime fingerprint for a configuration
// resolved at startup, where no credential generation is tracked yet.
func fingerprintFromMetadata(metadata domain.EmbeddingIndexMetadata) EmbeddingFingerprint {
	return EmbeddingFingerprint{
		Provider:        metadata.Provider,
		Model:           metadata.Model,
		Dimension:       metadata.Dimension,
		TemplateVersion: metadata.TemplateVersion,
		Distance:        metadata.Distance,
	}
}

// Reconcile validates the persisted dense index against the current
// configuration. When embeddings are disabled it is a no-op (old vectors are
// preserved but never used). When enabled it probes the dimension and, on any
// mismatch or a legacy index, rebuilds every vector in a single transactional
// commit so the published snapshot holds one compatible configuration. A rebuild
// failure is returned so readiness can fail rather than serve a partial index.
func (h *Hybrid) Reconcile(ctx context.Context, providerID, model string) error {
	embeddingSnapshot := h.embeddings.load()
	if embeddingSnapshot.State == EmbeddingDisabled {
		return nil
	}
	if !embeddingSnapshot.provider.Available() {
		return ai.ErrUnavailable
	}
	dimension, err := h.probeDimension(ctx, embeddingSnapshot.provider)
	if err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return err
	}
	desired := DesiredMetadata(providerID, model, dimension)
	if embeddingSnapshot.Fingerprint != (EmbeddingFingerprint{}) {
		desired = embeddingSnapshot.Fingerprint.Metadata()
		if desired.Dimension != dimension {
			h.markEmbeddingFailed(embeddingSnapshot)
			return fmt.Errorf("embedding dimension changed after preparation")
		}
	}
	metadata, err := h.store.EmbeddingMetadataContext(ctx)
	if err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return err
	}
	if MetadataCompatible(metadata, desired) && h.store.EmbeddingCount() > 0 {
		h.markEmbeddingAvailable(embeddingSnapshot)
		return nil
	}
	if err := h.rebuildAll(ctx, desired, embeddingSnapshot, nil); err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return err
	}
	h.markEmbeddingAvailable(embeddingSnapshot)
	return nil
}

// RebuildEmbeddings regenerates every vector even when the persisted metadata is
// already compatible. The configured provider/model are taken from the index
// metadata established by startup reconciliation, so this is an explicit
// maintenance operation rather than another incremental repository scan.
func (h *Hybrid) RebuildEmbeddings(ctx context.Context) error {
	return h.RebuildEmbeddingsWithProgress(ctx, nil)
}

// RebuildEmbeddingsWithProgress is RebuildEmbeddings with an optional progress
// callback so long-running rebuilds can be surfaced as job progress.
func (h *Hybrid) RebuildEmbeddingsWithProgress(ctx context.Context, progress EmbeddingProgress) error {
	embeddingSnapshot := h.embeddings.load()
	if embeddingSnapshot.State == EmbeddingDisabled {
		return fmt.Errorf("embeddings are disabled")
	}
	if !embeddingSnapshot.provider.Available() {
		return ai.ErrUnavailable
	}
	metadata, err := h.store.EmbeddingMetadataContext(ctx)
	if err != nil {
		return err
	}
	desired := metadata
	if embeddingSnapshot.Fingerprint != (EmbeddingFingerprint{}) {
		desired = embeddingSnapshot.Fingerprint.Metadata()
	}
	if strings.TrimSpace(desired.Provider) == "" || strings.TrimSpace(desired.Model) == "" {
		return fmt.Errorf("embedding index metadata is not configured")
	}
	dimension, err := h.probeDimension(ctx, embeddingSnapshot.provider)
	if err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return err
	}
	if embeddingSnapshot.Fingerprint != (EmbeddingFingerprint{}) && embeddingSnapshot.Fingerprint.Dimension != dimension {
		h.markEmbeddingFailed(embeddingSnapshot)
		return fmt.Errorf("embedding dimension changed after preparation")
	}
	desired.Dimension = dimension
	if err := h.rebuildAll(ctx, desired, embeddingSnapshot, progress); err != nil {
		h.markEmbeddingFailed(embeddingSnapshot)
		return err
	}
	h.markEmbeddingAvailable(embeddingSnapshot)
	return nil
}

func (h *Hybrid) probeDimension(ctx context.Context, provider ai.Provider) (int, error) {
	vectors, err := provider.Embed(ctx, []string{"codeatlas embedding dimension probe"})
	if err != nil {
		return 0, err
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return 0, fmt.Errorf("invalid embedding probe response")
	}
	return len(vectors[0]), nil
}

func (h *Hybrid) rebuildAll(ctx context.Context, desired domain.EmbeddingIndexMetadata, embeddingSnapshot *EmbeddingSnapshot, progress EmbeddingProgress) error {
	view, err := h.store.SnapshotContext(ctx)
	if err != nil {
		return err
	}
	symbols := view.AllSymbols()
	_ = view.Close()
	availableSnapshot := &EmbeddingSnapshot{State: EmbeddingAvailable, Fingerprint: embeddingSnapshot.Fingerprint, provider: embeddingSnapshot.provider}
	vectors, err := h.generateEmbeddings(ctx, symbols, availableSnapshot, progress)
	if err != nil {
		return err
	}
	desired.BuiltAt = time.Now().UTC()
	prepared, err := h.store.PrepareEmbeddingRebuildContext(ctx, vectors, desired)
	if err != nil {
		return err
	}
	_, err = h.store.CommitPreparedContext(ctx, prepared)
	return err
}

func (h *Hybrid) markEmbeddingAvailable(snapshot *EmbeddingSnapshot) {
	if snapshot.Fingerprint != (EmbeddingFingerprint{}) {
		h.embeddings.MarkAvailable(snapshot.Fingerprint)
	}
}

func (h *Hybrid) markEmbeddingFailed(snapshot *EmbeddingSnapshot) {
	if snapshot.Fingerprint != (EmbeddingFingerprint{}) {
		h.embeddings.MarkFailed(snapshot.Fingerprint)
	}
}
