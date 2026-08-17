package lspclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func TestStartContextDoesNotOwnProcessLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(ProcessConfig{
		Executable:      os.Args[0],
		Args:            []string{"-test.run=TestLSPClientHelperProcess", "--"},
		Env:             append(os.Environ(), "CODEATLAS_LSPCLIENT_HELPER=1"),
		ShutdownTimeout: time.Second,
	})
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if state := client.State(); state != StateRunning {
			t.Fatalf("state after startup context cancellation = %s, want running", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartRejectsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(ProcessConfig{Executable: os.Args[0]})

	if err := client.Start(ctx); err != context.Canceled {
		t.Fatalf("Start error = %v, want context.Canceled", err)
	}
	if pid := client.PID(); pid != 0 {
		t.Fatalf("PID = %d, want no process", pid)
	}
}

func TestHandlersRegisteredBeforeStartReceiveMessages(t *testing.T) {
	client := NewClient(ProcessConfig{
		Executable:      os.Args[0],
		Args:            []string{"-test.run=TestLSPClientHelperProcess", "--"},
		Env:             append(os.Environ(), "CODEATLAS_LSPCLIENT_HELPER=handlers"),
		ShutdownTimeout: time.Second,
	})
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	seen := make(chan string, 2)
	client.OnNotification("test/notification", func(json.RawMessage) {
		seen <- "notification"
	})
	client.OnRequest("test/request", func(context.Context, json.RawMessage) (any, error) {
		seen <- "request"
		return map[string]bool{"handled": true}, nil
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := map[string]bool{"notification": false, "request": false}
	deadline := time.After(2 * time.Second)
	for range 2 {
		select {
		case kind := <-seen:
			want[kind] = true
		case <-deadline:
			t.Fatalf("pre-start handlers invoked = %v, want notification and request", want)
		}
	}
}

func TestLSPClientHelperProcess(t *testing.T) {
	mode := os.Getenv("CODEATLAS_LSPCLIENT_HELPER")
	if mode == "" {
		return
	}
	if mode == "handlers" {
		writeHelperMessage(`{"jsonrpc":"2.0","method":"test/notification","params":{}}`)
		writeHelperMessage(`{"jsonrpc":"2.0","id":1,"method":"test/request","params":{}}`)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func writeHelperMessage(payload string) {
	_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
}
