package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

type Controller struct {
	Factory WindowFactory
	Server  ServerFunc
}

type serverState struct {
	mu  sync.Mutex
	err error
}

func (s *serverState) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *serverState) get() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (c Controller) Run(parent context.Context) error {
	window, err := c.Factory.New()
	if err != nil {
		c.Factory.ShowFatal("CodeAtlas could not start", safeFatalText(err.Error()))
		return err
	}
	defer window.Destroy()
	window.SetTitle("CodeAtlas")
	window.SetSize(1280, 800, SizeDefault)
	window.SetSize(900, 600, SizeMinimum)

	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	listening := make(chan net.Addr, 1)
	serverFinished := make(chan struct{})
	state := &serverState{}
	var notifyOnce sync.Once
	go func() {
		state.set(c.Server(runCtx, func(addr net.Addr) {
			notifyOnce.Do(func() { listening <- addr })
		}))
		close(serverFinished)
	}()

	select {
	case addr := <-listening:
		navigationURL, navigationErr := NavigationURL(addr)
		if navigationErr != nil {
			cancel()
			window.SetHTML(FatalHTML("CodeAtlas could not start", navigationErr))
			return c.runFatalWindow(parent, window, serverFinished, navigationErr)
		}
		window.Navigate(navigationURL)
	case <-serverFinished:
		serverErr := state.get()
		if serverErr == nil {
			serverErr = errors.New("server stopped before binding its listener")
		}
		window.SetHTML(FatalHTML("CodeAtlas could not start", serverErr))
		return c.runFatalWindow(parent, window, serverFinished, serverErr)
	case <-parent.Done():
		cancel()
		<-serverFinished
		return parent.Err()
	}

	windowClosed := make(chan struct{})
	serverMonitorDone := make(chan struct{})
	go func() {
		defer close(serverMonitorDone)
		select {
		case <-serverFinished:
			if parent.Err() != nil {
				return
			}
			select {
			case <-windowClosed:
				return
			default:
			}
			if serverErr := state.get(); serverErr != nil {
				window.Dispatch(func() {
					window.SetHTML(FatalHTML("CodeAtlas stopped", serverErr))
				})
			}
		case <-windowClosed:
		}
	}()
	parentMonitorDone := make(chan struct{})
	go func() {
		defer close(parentMonitorDone)
		select {
		case <-parent.Done():
			window.Dispatch(window.Terminate)
		case <-windowClosed:
		}
	}()

	window.Run()
	close(windowClosed)
	cancel()
	<-serverFinished
	<-serverMonitorDone
	<-parentMonitorDone
	if parent.Err() != nil {
		return parent.Err()
	}
	return state.get()
}

func (c Controller) runFatalWindow(parent context.Context, window Window, serverFinished <-chan struct{}, original error) error {
	windowClosed := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		select {
		case <-parent.Done():
			window.Dispatch(window.Terminate)
		case <-windowClosed:
		}
	}()
	window.Run()
	close(windowClosed)
	<-serverFinished
	<-monitorDone
	if parent.Err() != nil {
		return parent.Err()
	}
	if original == nil {
		return fmt.Errorf("CodeAtlas stopped unexpectedly")
	}
	return original
}
