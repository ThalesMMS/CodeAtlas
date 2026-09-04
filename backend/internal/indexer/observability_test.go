package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
)

func fieldFromLogLine(logged, msg, field string) string {
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["msg"] == msg {
			if value, ok := entry[field].(string); ok {
				return value
			}
		}
	}
	return ""
}

func TestScanEmitsCorrelatedObservability(t *testing.T) {
	t.Parallel()
	indexer, root, _ := newTestIndexer(t, codeparser.New())
	writeSource(t, root, "pkg/app.go", goSource)

	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	metrics := observability.NewMetrics()
	indexer.SetObservability(logger, metrics)

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	logged := buffer.String()
	if started := strings.Count(logged, `"msg":"index scan started"`); started != 1 {
		t.Fatalf("scan started logged %d times, want 1:\n%s", started, logged)
	}
	if completed := strings.Count(logged, `"msg":"index scan completed"`); completed != 1 {
		t.Fatalf("scan completed logged %d times, want 1:\n%s", completed, logged)
	}
	// operationId correlates the begin/end pair of a single scan.
	startID := fieldFromLogLine(logged, "index scan started", "operationId")
	endID := fieldFromLogLine(logged, "index scan completed", "operationId")
	if startID == "" || startID != endID {
		t.Fatalf("operationId mismatch: start=%q end=%q", startID, endID)
	}
	if !strings.Contains(logged, `"filesChanged":1`) {
		t.Fatalf("completed line missing filesChanged count:\n%s", logged)
	}
	// One scan must not produce a log line per file/symbol.
	if lines := strings.Count(logged, `"msg"`); lines > 4 {
		t.Fatalf("too many log lines for one scan (per-entity spam?): %d\n%s", lines, logged)
	}

	snapshot := metrics.Snapshot()
	if snapshot.IndexScansTotal != 1 || snapshot.IndexSuccessTotal != 1 || snapshot.CommitsTotal != 1 {
		t.Fatalf("metrics = %+v, want 1 scan / 1 success / 1 commit", snapshot)
	}
	if snapshot.IndexSize < 1 {
		t.Fatalf("index size not recorded: %+v", snapshot)
	}
}

func TestScanParseFailureLogsQuarantineAndCountsSuccess(t *testing.T) {
	t.Parallel()
	indexer, root, _ := newTestIndexer(t, failingParser{})
	writeSource(t, root, "pkg/app.go", goSource)

	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	metrics := observability.NewMetrics()
	indexer.SetObservability(logger, metrics)

	if err := indexer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	logged := buffer.String()
	if !strings.Contains(logged, `"msg":"index file quarantined"`) || !strings.Contains(logged, `"path":"pkg/app.go"`) {
		t.Fatalf("quarantine log missing msg/path:\n%s", logged)
	}
	if !strings.Contains(logged, `"msg":"index scan completed"`) {
		t.Fatalf("success log missing after quarantine:\n%s", logged)
	}
	if snapshot := metrics.Snapshot(); snapshot.IndexSuccessTotal != 1 || snapshot.IndexFailureTotal != 0 {
		t.Fatalf("metrics = %+v, want 1 success and 0 failures", snapshot)
	}
}
