package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/capabilities"
	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

// RuntimeDeps are the injectable ports of the composition root. Production wires
// concrete implementations in cmd/codeatlas; tests inject fakes to exercise the
// startup/readiness failure matrix without external network or shell processes.
type RuntimeDeps struct {
	Logger              *slog.Logger
	Coordinator         *readiness.Coordinator
	Registry            *capabilities.Registry
	Probes              []capabilities.Probe
	ProviderProbe       ai.CapabilityProbe
	EnableEmbeddings    bool
	MigrateStore        func(context.Context) error
	InitialIndex        func(context.Context) error
	RunIndexer          func(context.Context)
	ReconcileEmbeddings func(context.Context) error
	Server              *http.Server
	Listen              func() (net.Listener, error)
	Persist             func() error
	ShutdownTimeout     time.Duration
	// RecoveryError, when non-nil, marks a failed startup workspace-transaction
	// recovery; the bootstrap fails immediately without probing.
	RecoveryError error
}

// Run owns the runtime lifecycle: bind the listener (a bind failure is fatal and
// returned), serve the diagnostic/functional HTTP surface, drive the readiness
// bootstrap in a separate goroutine, and perform a graceful shutdown without
// leaking goroutines. It never calls os.Exit; the caller decides the exit code.
func Run(ctx context.Context, deps RuntimeDeps) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	listener, err := deps.Listen()
	if err != nil {
		return fmt.Errorf("bind/listen: %w", err)
	}

	serveError := make(chan error, 1)
	go func() {
		if err := deps.Server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveError <- err
		}
	}()

	bootstrapDone := make(chan struct{})
	go func() {
		defer close(bootstrapDone)
		runBootstrap(runCtx, bootstrapDeps{
			logger:              deps.Logger,
			coordinator:         deps.Coordinator,
			registry:            deps.Registry,
			probes:              deps.Probes,
			providerProbe:       deps.ProviderProbe,
			enableEmbeddings:    deps.EnableEmbeddings,
			migrateStore:        deps.MigrateStore,
			initialIndex:        deps.InitialIndex,
			runIndexer:          deps.RunIndexer,
			reconcileEmbeddings: deps.ReconcileEmbeddings,
			recoveryError:       deps.RecoveryError,
		})
	}()

	var runErr error
	select {
	case <-runCtx.Done():
	case err := <-serveError:
		runErr = err
		_ = deps.Coordinator.Fail("HTTP_SERVE_FAILED", "HTTP server stopped unexpectedly")
		cancel() // stop the bootstrap goroutine
	}

	_ = deps.Coordinator.Shutdown("encerrando")
	timeout := deps.ShutdownTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), timeout)
	defer shutdownCancel()
	if err := deps.Server.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}
	<-bootstrapDone // ensure the bootstrap/indexer goroutine has stopped
	if deps.Persist != nil {
		if err := deps.Persist(); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}
