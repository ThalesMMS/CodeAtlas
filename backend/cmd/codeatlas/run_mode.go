package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ThalesMMS/CodeAtlas/internal/config"
	"github.com/ThalesMMS/CodeAtlas/internal/desktop"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	"github.com/ThalesMMS/CodeAtlas/internal/settings"
)

func run() int {
	return runProcess(os.Args[1:], desktop.PrepareHeadlessConsole, runStoreCommand, desktop.NativeFactory)
}

func runProcess(args []string, prepareConsole func() error, storeCommand func([]string) int, factoryProvider func() desktop.WindowFactory) int {
	if len(args) > 0 && args[0] == "store" {
		if err := prepareConsole(); err != nil {
			fmt.Fprintln(os.Stderr, observability.RedactString(err.Error()))
			return 1
		}
		return storeCommand(args[1:])
	}

	factory := factoryProvider()
	mode, err := desktop.ParseMode(args, desktop.DefaultEnabled())
	if err != nil {
		return presentStartupError(mode.Enabled, factory, err)
	}
	if !mode.Enabled {
		if err := prepareConsole(); err != nil {
			fmt.Fprintln(os.Stderr, observability.RedactString(err.Error()))
			return 1
		}
	}
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := func(ctx context.Context, listening func(net.Addr)) error {
		return runConfiguredForMode(ctx, mode.Args, mode.Enabled, listening)
	}
	return exitCode(runSelectedMode(rootContext, mode, factory, server))
}

func runSelectedMode(ctx context.Context, mode desktop.Mode, factory desktop.WindowFactory, server desktop.ServerFunc) error {
	if !mode.Enabled {
		return runHeadless(ctx, server)
	}
	return (desktop.Controller{Factory: factory, Server: server}).Run(ctx)
}

// runHeadless runs the server until it stops for a reason other than an
// in-process restart request. The desktop controller applies the same loop
// while keeping its window open.
func runHeadless(ctx context.Context, server desktop.ServerFunc) error {
	for {
		err := server(ctx, nil)
		if !errors.Is(err, desktop.ErrRestartRequested) || ctx.Err() != nil {
			return err
		}
	}
}

func runConfigured(ctx context.Context, args []string, onListening func(net.Addr)) error {
	return runConfiguredForMode(ctx, args, false, onListening)
}

func runConfiguredForMode(ctx context.Context, args []string, desktopEnabled bool, onListening func(net.Addr)) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// A settings restart cancels this run with ErrRestartRequested as the cause
	// so the mode runner can start the composition again with the saved values.
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	requestRestart := func() {
		logger.Info("restart requested from Settings; reloading the saved configuration")
		cancelRun(desktop.ErrRestartRequested)
	}
	err := runConfiguredComposition(runCtx, args, desktopEnabled, onListening, requestRestart, logger)
	return restartOutcome(runCtx, err)
}

// restartOutcome maps a run that was cancelled by a restart request to
// ErrRestartRequested, regardless of how the runtime reported its shutdown.
func restartOutcome(runCtx context.Context, err error) error {
	if errors.Is(context.Cause(runCtx), desktop.ErrRestartRequested) {
		return desktop.ErrRestartRequested
	}
	return err
}

func runConfiguredComposition(ctx context.Context, args []string, desktopEnabled bool, onListening func(net.Addr), requestRestart func(), logger *slog.Logger) error {
	settingsPath, err := settings.DefaultPath()
	if err != nil {
		logger.Error("could not resolve per-user settings path", "error", err)
		return fmt.Errorf("resolve per-user settings path: %w", err)
	}
	settingsStore := settings.NewFileStore(settingsPath)
	credentialStore := settings.NewKeyringCredentialStore()
	settingsEnvironment := settings.EnvironmentFromLookup(os.LookupEnv)
	_, startupResolved, err := settings.LoadStartup(ctx, settingsEnvironment, settingsStore, credentialStore)
	if err != nil {
		logger.Error("could not load per-user settings", "error", err)
		return fmt.Errorf("load per-user settings: %w", err)
	}
	if desktopEnabled && startupResolved.Source(settings.FieldWorkspace) == settings.SourceDefault {
		workspace, err := defaultDesktopWorkspace(settingsPath)
		if err != nil {
			logger.Error("could not create default desktop workspace", "error", err)
			return fmt.Errorf("create default desktop workspace: %w", err)
		}
		settingsEnvironment, startupResolved = applyDesktopWorkspaceDefault(settingsEnvironment, startupResolved, workspace)
	}
	cfg, err := config.LoadArgsWithSettings(args, startupResolved.Values)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return runComposition(ctx, cfg, settingsEnvironment, onListening, requestRestart, logger)
}

func defaultDesktopWorkspace(settingsPath string) (string, error) {
	workspace := filepath.Join(filepath.Dir(settingsPath), "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return "", err
	}
	return workspace, nil
}

func applyDesktopWorkspaceDefault(environment settings.Environment, resolved settings.Resolved, workspace string) (settings.Environment, settings.Resolved) {
	adjustedEnvironment := make(settings.Environment, len(environment)+1)
	for key, value := range environment {
		adjustedEnvironment[key] = value
	}
	adjustedEnvironment[settings.FieldWorkspace] = workspace
	resolved.Values.Workspace = workspace
	return adjustedEnvironment, resolved
}

func presentStartupError(desktopEnabled bool, factory desktop.WindowFactory, err error) int {
	message := observability.RedactString(err.Error())
	if desktopEnabled {
		factory.ShowFatal("CodeAtlas could not start", message)
	} else {
		fmt.Fprintln(os.Stderr, message)
	}
	return 1
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	return 1
}
