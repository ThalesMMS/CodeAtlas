package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/indexer"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/service"
	"github.com/ThalesMMS/CodeAtlas/internal/storemigrate"
)

type storeCommandConfig struct {
	workspace      string
	databasePath   string
	legacyJSONPath string
	maxFileBytes   int64
}

func runStoreCommand(args []string) int {
	if len(args) == 0 {
		printStoreUsage()
		return 2
	}
	command := args[0]
	cfg, remaining, err := parseStoreCommandFlags(command, args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx := context.Background()
	options := storemigrate.StartupOptions{
		WorkspaceRoot:  cfg.workspace,
		DatabasePath:   cfg.databasePath,
		LegacyJSONPath: cfg.legacyJSONPath,
		RecoverLegacyTransactions: func() error {
			journalDir := filepath.Join(filepath.Dir(cfg.databasePath), "transactions")
			return service.RecoverTransactionsForIndexes(cfg.workspace, journalDir, cfg.legacyJSONPath, cfg.databasePath)
		},
	}

	if len(remaining) != 0 {
		fmt.Fprintf(os.Stderr, "store %s does not accept positional arguments\n", command)
		return 2
	}

	switch command {
	case "plan":
		plan, err := storemigrate.PlanStartup(options)
		if err != nil {
			printStoreCommandError(err)
			return 1
		}
		return writeJSONStdout(plan)
	case "migrate":
		store, report, err := storemigrate.OpenSQLiteForStartup(ctx, options)
		if err != nil {
			printStoreCommandError(err)
			return 1
		}
		defer store.Close()
		return writeJSONStdout(report)
	case "rebuild":
		store, report, err := storemigrate.RebuildSQLite(ctx, options)
		if err != nil {
			printStoreCommandError(err)
			return 1
		}
		defer store.Close()
		scanner := indexer.New(cfg.workspace, cfg.maxFileBytes, codeparser.New(), store)
		if err := scanner.Scan(ctx); err != nil {
			printStoreCommandError(err)
			return 1
		}
		if err := store.Validate(ctx); err != nil {
			printStoreCommandError(err)
			return 1
		}
		files, symbols, edges, wiki, indexedAt := store.Counts()
		return writeJSONStdout(map[string]any{
			"migration": report,
			"counts": map[string]any{
				"files": files, "symbols": symbols, "edges": edges, "wiki": wiki, "indexedAt": indexedAt,
			},
		})
	case "verify":
		stateDir := filepath.Dir(cfg.databasePath)
		marker, ok, err := storemigrate.ReadMarker(stateDir)
		if err != nil {
			printStoreCommandError(err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "store backend marker is missing")
			return 1
		}
		if marker.Backend != storemigrate.BackendSQLite || marker.Database != filepath.Base(cfg.databasePath) {
			fmt.Fprintf(os.Stderr, "store backend marker does not match %s\n", cfg.databasePath)
			return 1
		}
		if err := storemigrate.VerifySQLite(ctx, cfg.workspace, cfg.databasePath); err != nil {
			printStoreCommandError(err)
			return 1
		}
		return writeJSONStdout(map[string]any{"ok": true, "marker": marker})
	default:
		printStoreUsage()
		return 2
	}
}

func parseStoreCommandFlags(command string, args []string) (storeCommandConfig, []string, error) {
	flags := flag.NewFlagSet("codeatlas store "+command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	workspaceFlag := flags.String("workspace", envOrDefault("CODEATLAS_WORKSPACE", "."), "workspace directory to index")
	dbFlag := flags.String("db", envOrDefault("CODEATLAS_DB", ""), "SQLite database path")
	maxFileBytesFlag := flags.Int64("max-file-bytes", int64(envIntDefault("CODEATLAS_MAX_FILE_BYTES", 1_500_000)), "maximum source file size")
	if err := flags.Parse(args); err != nil {
		return storeCommandConfig{}, nil, err
	}
	workspace, err := filepath.Abs(*workspaceFlag)
	if err != nil {
		return storeCommandConfig{}, nil, err
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return storeCommandConfig{}, nil, err
	}
	if !info.IsDir() {
		return storeCommandConfig{}, nil, fmt.Errorf("workspace is not a directory: %s", workspace)
	}
	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = filepath.Join(workspace, ".codeatlas", "codeatlas.db")
	}
	dbPath, err = filepath.Abs(dbPath)
	if err != nil {
		return storeCommandConfig{}, nil, err
	}
	cfg := storeCommandConfig{
		workspace:      workspace,
		databasePath:   dbPath,
		legacyJSONPath: filepath.Join(filepath.Dir(dbPath), "index.json"),
		maxFileBytes:   *maxFileBytesFlag,
	}
	return cfg, flags.Args(), nil
}

func printStoreUsage() {
	fmt.Fprintln(os.Stderr, "usage: codeatlas store <plan|migrate|rebuild|verify> [--workspace DIR] [--db PATH]")
}

func writeJSONStdout(value any) int {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}

func printStoreCommandError(err error) {
	fmt.Fprintln(os.Stderr, err)
	if cause := errors.Unwrap(err); cause != nil {
		fmt.Fprintln(os.Stderr, "cause:", cause)
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envIntDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
