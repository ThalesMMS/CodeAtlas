// Package app is the testable composition root for the CodeAtlas runtime. It
// wires the readiness coordinator, capability probes, initial indexing and the
// HTTP server into a single Run function that never calls os.Exit, so the whole
// startup/readiness lifecycle can be exercised by a failure matrix.
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

const (
	capabilityLLMChat       capabilities.CapabilityID = "llm-chat"
	capabilityLLMEmbeddings capabilities.CapabilityID = "llm-embeddings"
)

// bootstrapDeps are the seams the startup sequence depends on.
type bootstrapDeps struct {
	logger           *slog.Logger
	coordinator      *readiness.Coordinator
	registry         *capabilities.Registry
	probes           []capabilities.Probe
	providerProbe    ai.CapabilityProbe
	enableEmbeddings bool
	migrateStore     func(context.Context) error
	initialIndex     func(context.Context) error
	runIndexer       func(context.Context)
	// reconcileEmbeddings validates the dense index against the configuration
	// without generating vectors; it reports whether a background rebuild is
	// required once the process is READY.
	reconcileEmbeddings func(context.Context) (bool, error)
	// scheduleEmbeddingRebuild submits the coalescing embeddings.rebuild job.
	scheduleEmbeddingRebuild func(context.Context)
	recoveryError            error
}

// runBootstrap drives the readiness lifecycle:
//
//	BOOTING -> PROBING_CAPABILITIES -> MIGRATING_STORE -> INDEXING -> READY
//
// A mandatory local probe or initial-index failure transitions to FAILED. A
// provider-only failure enters AWAITING_CONFIGURATION and blocks on an explicit,
// coalesced retry signal from a successful Settings activation.
func runBootstrap(ctx context.Context, deps bootstrapDeps) {
	if ctx.Err() != nil {
		return
	}
	// A failed workspace-transaction recovery is fatal before any probing.
	if deps.recoveryError != nil {
		_ = deps.coordinator.Fail("WORKSPACE_TRANSACTION_RECOVERY_FAILED", "workspace transaction recovery failed")
		deps.logger.Error("transaction recovery failed", "error", deps.recoveryError)
		return
	}
	deps.coordinator.SetStep("probing capabilities")
	if err := deps.coordinator.Transition(readiness.StateProbingCapabilities, "probing capabilities"); err != nil {
		return
	}

	failure, failed := probeLocalMandatory(ctx, deps)
	if ctx.Err() != nil {
		return // cancelled during probing; do not mark FAILED
	}
	if failed {
		_ = deps.coordinator.Fail(failure.ErrorCode, "required capability unavailable: "+string(failure.ID))
		deps.logger.Error("required probe failed", "capability", string(failure.ID), "code", failure.ErrorCode)
		return
	}

	// Open the repository before waiting for provider configuration. Settings can
	// enable embeddings while the process is in AWAITING_CONFIGURATION, and that
	// live preparation needs access to the persisted embedding metadata. Delaying
	// store migration until after the provider probe made first-run configuration
	// fail with EMBEDDING_STORE_UNAVAILABLE even when the endpoint probe passed.
	if deps.migrateStore != nil {
		deps.coordinator.SetStep("migrating store")
		if err := deps.coordinator.Transition(readiness.StateMigratingStore, "migrating store"); err != nil {
			return
		}
		if err := deps.migrateStore(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = deps.coordinator.Fail("STORE_MIGRATION_FAILED", "store backend migration failed")
			deps.logger.Error("store migration failed", "error", err)
			return
		}
		if ctx.Err() != nil {
			return
		}
	}

	for {
		failure, failed = probeProviderMandatory(ctx, deps)
		if ctx.Err() != nil {
			return
		}
		if !failed {
			break
		}
		deps.coordinator.SetStep("awaiting provider configuration")
		if err := deps.coordinator.Transition(readiness.StateAwaitingConfiguration, "provider configuration required"); err != nil {
			return
		}
		deps.logger.Warn("provider configuration required", "capability", string(failure.ID), "code", failure.ErrorCode)
		select {
		case <-ctx.Done():
			return
		case <-deps.coordinator.ConfigurationRetries():
		}
		deps.coordinator.SetStep("probing provider configuration")
		if err := deps.coordinator.Transition(readiness.StateProbingCapabilities, "retry provider configuration"); err != nil {
			return
		}
	}

	// Reconcile the dense index before indexing: a compatible index stays live
	// and incremental scans keep embedding their deltas, while a legacy or
	// incompatible index puts the runtime into the rebuilding state so the
	// initial scan commits without vectors and READY is never blocked on the
	// embedding provider. The actual rebuild runs as a background job.
	needsEmbeddingRebuild := false
	if deps.reconcileEmbeddings != nil {
		deps.coordinator.SetStep("reconciling embeddings")
		rebuild, err := deps.reconcileEmbeddings(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = deps.coordinator.Fail("EMBEDDING_REBUILD_FAILED", "embeddings reconciliation failed")
			deps.logger.Error("embeddings reconciliation failed", "error", err)
			return
		}
		needsEmbeddingRebuild = rebuild
	}
	if ctx.Err() != nil {
		return
	}

	deps.coordinator.SetStep("indexing workspace")
	if err := deps.coordinator.Transition(readiness.StateIndexing, "initial indexing"); err != nil {
		return
	}
	if err := deps.initialIndex(ctx); err != nil {
		if ctx.Err() != nil {
			return // cancelled, not a real failure
		}
		_ = deps.coordinator.Fail("INITIAL_INDEX_FAILED", "initial indexing failed")
		deps.logger.Error("initial indexing failed", "error", err)
		return
	}
	if ctx.Err() != nil {
		return
	}

	deps.coordinator.SetStep("ready")
	if err := deps.coordinator.Transition(readiness.StateReady, "startup complete"); err != nil {
		return
	}
	deps.logger.Info("CodeAtlas READY")

	if needsEmbeddingRebuild && deps.scheduleEmbeddingRebuild != nil {
		deps.logger.Info("scheduling background embeddings rebuild")
		deps.scheduleEmbeddingRebuild(ctx)
	}

	if deps.runIndexer != nil {
		deps.runIndexer(ctx) // blocks until ctx is cancelled
	}
}

func probeLocalMandatory(ctx context.Context, deps bootstrapDeps) (capabilities.Result, bool) {
	results := capabilities.Runner{}.Run(ctx, deps.probes, deps.registry)
	for _, result := range results {
		if result.Requirement == capabilities.Required && result.State != capabilities.CapabilityAvailable {
			return result, true
		}
	}
	return capabilities.Result{}, false
}

func probeProviderMandatory(ctx context.Context, deps bootstrapDeps) (capabilities.Result, bool) {
	chatProbe := ai.ProviderProbeResult{Status: ai.ProbeFailure, ErrorCode: "PROVIDER_UNCONFIGURED", Message: "provider is not configured"}
	embeddingProbe := ai.ProviderProbeResult{Status: ai.ProbeDisabled, Message: "embeddings are disabled"}
	if deps.providerProbe != nil {
		chatProbe = deps.providerProbe.ProbeChat(ctx)
		embeddingProbe = deps.providerProbe.ProbeEmbeddings(ctx)
	}
	chat := providerCapability(capabilityLLMChat, capabilities.Required, chatProbe)
	deps.registry.UpdateCapability(chat)

	embeddingRequirement := capabilities.Optional
	if deps.enableEmbeddings {
		embeddingRequirement = capabilities.Required
	}
	embeddings := providerCapability(capabilityLLMEmbeddings, embeddingRequirement, embeddingProbe)
	deps.registry.UpdateCapability(embeddings)

	for _, result := range []capabilities.Result{chat, embeddings} {
		if result.Requirement == capabilities.Required && result.State != capabilities.CapabilityAvailable {
			return result, true
		}
	}
	return capabilities.Result{}, false
}

// providerCapability converts an AI provider probe result into a capability
// result so it can be recorded in the same registry as the local probes.
func providerCapability(id capabilities.CapabilityID, requirement capabilities.Requirement, result ai.ProviderProbeResult) capabilities.Result {
	state := capabilities.CapabilityUnavailable
	switch result.Status {
	case ai.ProbeSuccess:
		state = capabilities.CapabilityAvailable
	case ai.ProbeDisabled:
		state = capabilities.CapabilityDisabled
	}
	return capabilities.Result{
		ID:          id,
		Requirement: requirement,
		State:       state,
		Duration:    result.Duration,
		CheckedAt:   time.Now().UTC(),
		ErrorCode:   result.ErrorCode,
		Message:     result.Message,
		Metadata:    result.Metadata,
	}
}
