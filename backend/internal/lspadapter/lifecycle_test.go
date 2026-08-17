package lspadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/lspclient"
)

type lifecycleClient struct {
	startErr error
	closed   bool
}

func (c *lifecycleClient) Start(context.Context) error                          { return c.startErr }
func (c *lifecycleClient) Request(context.Context, string, any, any) error      { return nil }
func (c *lifecycleClient) Notify(context.Context, string, any) error            { return nil }
func (c *lifecycleClient) OnNotification(string, lspclient.NotificationHandler) {}
func (c *lifecycleClient) OnRequest(string, lspclient.ServerRequestHandler)     {}
func (c *lifecycleClient) State() lspclient.ClientState                         { return lspclient.StateNew }
func (c *lifecycleClient) Close(context.Context) error {
	c.closed = true
	return nil
}

func TestStartClosesClientAfterStartFailure(t *testing.T) {
	t.Parallel()
	startFailure := errors.New("start failed")
	client := &lifecycleClient{startErr: startFailure}
	started, version, err := Start(context.Background(), StartConfig{
		Executable:   "language-server",
		StartTimeout: time.Second,
		Probe:        func(context.Context, string) (string, error) { return "1.0", nil },
		Factory:      func(lspclient.ProcessConfig) Client { return client },
		Initialize:   func(context.Context, Client) error { return nil },
	})
	if started != nil || version != "1.0" || !errors.Is(err, startFailure) {
		t.Fatalf("Start() = client %v, version %q, error %v", started, version, err)
	}
	var lifecycleErr *StartError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.Stage != StageStart {
		t.Fatalf("Start() error = %v, want StageStart", err)
	}
	if !client.closed {
		t.Fatal("client was not closed after Start failure")
	}
}
