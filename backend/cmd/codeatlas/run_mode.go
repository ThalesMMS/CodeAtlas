package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
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
		return runConfigured(ctx, mode.Args, listening)
	}
	return exitCode(runSelectedMode(rootContext, mode, factory, server))
}

func runSelectedMode(ctx context.Context, mode desktop.Mode, factory desktop.WindowFactory, server desktop.ServerFunc) error {
	if !mode.Enabled {
		return server(ctx, nil)
	}
	return (desktop.Controller{Factory: factory, Server: server}).Run(ctx)
}

func runConfigured(ctx context.Context, args []string, onListening func(net.Addr)) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	cfg, err := config.LoadArgsWithSettings(args, startupResolved.Values)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return runComposition(ctx, cfg, onListening, logger)
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
