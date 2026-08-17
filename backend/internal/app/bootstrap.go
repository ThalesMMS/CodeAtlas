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
	logger              *slog.Logger
	coordinator         *readiness.Coordinator
	registry            *capabilities.Registry
	probes              []capabilities.Probe
	providerProbe       ai.CapabilityProbe
	enableEmbeddings    bool
	migrateStore        func(context.Context) error
	initialIndex        func(context.Context) error
	runIndexer          func(context.Context)
	reconcileEmbeddings func(context.Context) error
	recoveryError       error
}

// runBootstrap drives the readiness lifecycle:
//
//	BOOTING -> PROBING_CAPABILITIES -> INDEXING -> READY
//
// A mandatory probe failure or an initial-index failure transitions to FAILED
// with a structured cause and stops; there is no silent retry. The function
// honors context cancellation at every step and never reaches READY before the
// initial index has been persisted. After READY it starts the periodic indexer,
// which blocks until the context is cancelled.
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

	failure, failed := probeMandatory(ctx, deps)
	if ctx.Err() != nil {
		return // cancelled during probing; do not mark FAILED
	}
	if failed {
		_ = deps.coordinator.Fail(failure.ErrorCode, "required capability unavailable: "+string(failure.ID))
		deps.logger.Error("required probe failed", "capability", string(failure.ID), "code", failure.ErrorCode)
		return
	}

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

	// Reconcile the dense index (rebuild on incompatible/legacy embeddings) before
	// READY, so dense retrieval is never promised over a mismatched index.
	if deps.reconcileEmbeddings != nil {
		deps.coordinator.SetStep("reconciling embeddings")
		if err := deps.reconcileEmbeddings(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = deps.coordinator.Fail("EMBEDDING_REBUILD_FAILED", "embeddings rebuild failed")
			deps.logger.Error("embeddings reconciliation failed", "error", err)
			return
		}
	}
	if ctx.Err() != nil {
		return
	}

	deps.coordinator.SetStep("ready")
	if err := deps.coordinator.Transition(readiness.StateReady, "startup complete"); err != nil {
		return
	}
	deps.logger.Info("CodeAtlas READY")

	if deps.runIndexer != nil {
		deps.runIndexer(ctx) // blocks until ctx is cancelled
	}
}

// probeMandatory runs every probe (local capabilities plus the provider) into
// the registry and returns the first mandatory failure, if any.
func probeMandatory(ctx context.Context, deps bootstrapDeps) (capabilities.Result, bool) {
	results := capabilities.Runner{}.Run(ctx, deps.probes, deps.registry)

	if deps.providerProbe != nil {
		chat := providerCapability(capabilityLLMChat, capabilities.Required, deps.providerProbe.ProbeChat(ctx))
		deps.registry.UpdateCapability(chat)
		results = append(results, chat)

		embeddingRequirement := capabilities.Optional
		if deps.enableEmbeddings {
			embeddingRequirement = capabilities.Required
		}
		embeddings := providerCapability(capabilityLLMEmbeddings, embeddingRequirement, deps.providerProbe.ProbeEmbeddings(ctx))
		deps.registry.UpdateCapability(embeddings)
		results = append(results, embeddings)
	}

	for _, result := range results {
		if result.Requirement == capabilities.Required && result.State == capabilities.CapabilityUnavailable {
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
