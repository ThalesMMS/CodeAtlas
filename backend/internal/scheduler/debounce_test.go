package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestDebouncerCoalescesWritesAndFlushesOnQuietWindow(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	var flushed []domain.ReconcileRequest
	debouncer := NewDebouncer(DebounceConfig{
		Clock:       clock,
		QuietWindow: 250 * time.Millisecond,
		MaxDelay:    2 * time.Second,
		Flush: func(req domain.ReconcileRequest) {
			flushed = append(flushed, req)
		},
	})

	debouncer.Submit(domain.ChangeHint{Sequence: 7, Path: "./pkg/a.go", Operations: domain.OpWrite, Kind: "file"})
	clock.Advance(200 * time.Millisecond)
	debouncer.Submit(domain.ChangeHint{Sequence: 8, Path: "pkg/a.go", Operations: domain.OpCreate, Kind: "file"})
	clock.Advance(249 * time.Millisecond)
	if len(flushed) != 0 {
		t.Fatalf("flushed before quiet window: %#v", flushed)
	}

	clock.Advance(time.Millisecond)
	if len(flushed) != 1 {
		t.Fatalf("flush count = %d, want 1", len(flushed))
	}
	scope := flushed[0].Scope
	if scope.Mode != domain.ReconcilePaths ||
		!reflect.DeepEqual(scope.Paths, []string{"pkg/a.go"}) ||
		scope.MinSequence != 7 ||
		scope.MaxSequence != 8 {
		t.Fatalf("scope = %#v, want one canonical path with sequence bounds", scope)
	}
}

func TestDebouncerFlushesContinuousStreamByMaxDelay(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	var scopes []domain.ReconcileScope
	debouncer := NewDebouncer(DebounceConfig{
		Clock:       clock,
		QuietWindow: 250 * time.Millisecond,
		MaxDelay:    time.Second,
		Flush: func(req domain.ReconcileRequest) {
			scopes = append(scopes, req.Scope)
		},
	})

	for seq := uint64(1); seq <= 5; seq++ {
		debouncer.Submit(domain.ChangeHint{Sequence: seq, Path: "pkg/a.go", Operations: domain.OpWrite, Kind: "file"})
		clock.Advance(200 * time.Millisecond)
	}
	if len(scopes) != 1 {
		t.Fatalf("flush count = %d, want max-delay flush", len(scopes))
	}
	if scopes[0].MinSequence != 1 || scopes[0].MaxSequence != 5 {
		t.Fatalf("scope sequences = %d..%d, want 1..5", scopes[0].MinSequence, scopes[0].MaxSequence)
	}
}

func TestDebouncerDropsMetadataOnlyBatches(t *testing.T) {
	clock := NewManualClock(time.Unix(1700000000, 0).UTC())
	calls := 0
	debouncer := NewDebouncer(DebounceConfig{
		Clock:       clock,
		QuietWindow: 10 * time.Millisecond,
		MaxDelay:    time.Second,
		Flush: func(domain.ReconcileRequest) {
			calls++
		},
	})

	if debouncer.Submit(domain.ChangeHint{Sequence: 1, Path: "pkg/a.go", Operations: domain.OpChmod, Kind: "file"}) {
		t.Fatal("metadata-only hint was accepted")
	}
	clock.Advance(time.Second)
	if calls != 0 {
		t.Fatalf("flush calls = %d, want 0", calls)
	}
}
