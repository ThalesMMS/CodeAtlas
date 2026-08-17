package lspadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// Client is the common language-server transport interface used by all adapters.
type Client interface {
	Start(ctx context.Context) error
	Request(ctx context.Context, method string, params, result any) error
	Notify(ctx context.Context, method string, params any) error
	OnNotification(method string, handler lspclient.NotificationHandler)
	OnRequest(method string, handler lspclient.ServerRequestHandler)
	State() lspclient.ClientState
	Close(ctx context.Context) error
}

type ClientFactory func(lspclient.ProcessConfig) Client

type StartStage string

const (
	StageProbe      StartStage = "probe"
	StageStart      StartStage = "start"
	StageInitialize StartStage = "initialize"
)

// StartError identifies which shared lifecycle stage failed.
type StartError struct {
	Stage StartStage
	Err   error
}

func (e *StartError) Error() string { return fmt.Sprintf("lspadapter %s: %v", e.Stage, e.Err) }
func (e *StartError) Unwrap() error { return e.Err }

// StartConfig parameterizes process mechanics while language adapters retain
// protocol-specific handlers and initialization payloads.
type StartConfig struct {
	Executable     string
	Args           []string
	WorkingDir     string
	RequestTimeout time.Duration
	StartTimeout   time.Duration
	ProbeArgs      []string
	VersionPattern *regexp.Regexp
	Probe          func(context.Context, string) (string, error)
	Factory        ClientFactory
	Configure      func(Client)
	Initialize     func(context.Context, Client) error
}

// Start probes, starts and initializes one language-server process. A failed
// initialization always closes the process before returning.
func Start(ctx context.Context, config StartConfig) (Client, string, error) {
	probe := config.Probe
	if probe == nil {
		probe = func(ctx context.Context, executable string) (string, error) {
			return ProbeVersion(ctx, executable, config.ProbeArgs, config.VersionPattern)
		}
	}
	version, err := probe(ctx, config.Executable)
	if err != nil {
		return nil, "", &StartError{Stage: StageProbe, Err: err}
	}
	client := config.Factory(lspclient.ProcessConfig{
		Executable: config.Executable, Args: config.Args, WorkingDir: config.WorkingDir,
		RequestTimeout: config.RequestTimeout,
	})
	if config.Configure != nil {
		config.Configure(client)
	}
	startCtx, cancel := context.WithTimeout(ctx, config.StartTimeout)
	defer cancel()
	if err := client.Start(startCtx); err != nil {
		_ = client.Close(context.Background())
		return nil, version, &StartError{Stage: StageStart, Err: err}
	}
	if err := config.Initialize(startCtx, client); err != nil {
		_ = client.Close(context.Background())
		return nil, version, &StartError{Stage: StageInitialize, Err: err}
	}
	return client, version, nil
}

// ProbeVersion executes an allowlisted version command without a shell and
// bounds its output before matching.
func ProbeVersion(ctx context.Context, executable string, args []string, pattern *regexp.Regexp) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, executable, args...).Output()
	if err != nil {
		return "", err
	}
	bounded := []byte(textutil.TruncateUTF8(string(output), 4096))
	if pattern != nil {
		if match := pattern.Find(bounded); match != nil {
			return string(match), nil
		}
	}
	return strings.TrimSpace(string(bounded)), nil
}

// Shutdown performs the common LSP shutdown/exit handshake.
func Shutdown(ctx context.Context, client Client, requestTimeout time.Duration) error {
	if client == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_ = client.Request(shutdownCtx, "shutdown", nil, nil)
	_ = client.Notify(shutdownCtx, "exit", nil)
	return client.Close(ctx)
}

// ShouldStart implements the shared false/true/auto enable policy.
func ShouldStart(mode string, hasLanguageFiles bool) bool {
	return mode == "true" || (mode == "auto" && hasLanguageFiles)
}

// DenyWorkspaceEdit returns the fixed response shared by all adapters.
func DenyWorkspaceEdit(context.Context, json.RawMessage) (any, error) {
	return map[string]any{"applied": false, "failureReason": "CodeAtlas does not apply workspace edits"}, nil
}
