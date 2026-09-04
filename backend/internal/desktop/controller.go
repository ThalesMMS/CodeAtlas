package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// ApplicationName is the name the desktop shell shows for CodeAtlas: the
// window title and the menu bar's "Quit <name>" item.
const ApplicationName = "CodeAtlas"

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
	window.SetTitle(ApplicationName)
	window.SetSize(1280, 800, SizeDefault)
	window.SetSize(900, 600, SizeMinimum)
	installQuitMenu(window)
	bindFolderPicker(window)

	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	// Each server run reports its listener once; runs are sequential, so a
	// single buffered slot never blocks a shutting-down server.
	listening := make(chan net.Addr, 1)
	serverFinished := make(chan struct{})
	state := &serverState{}
	go func() {
		defer close(serverFinished)
		state.set(c.serve(runCtx, window, listening))
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
		for {
			select {
			case addr := <-listening:
				// A restarted server bound a (possibly new) listener: point the
				// existing window at it.
				navigationURL, navigationErr := NavigationURL(addr)
				window.Dispatch(func() {
					if navigationErr != nil {
						window.SetHTML(FatalHTML("CodeAtlas could not restart", navigationErr))
						return
					}
					window.Navigate(navigationURL)
				})
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
				return
			case <-windowClosed:
				return
			}
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

// serve runs the server until it stops for a reason other than a restart
// request. A restart tears the composition down and starts it again in the
// same process, so saved restart-only settings (such as the workspace) take
// effect without relaunching the application.
func (c Controller) serve(ctx context.Context, window Window, listening chan<- net.Addr) error {
	for {
		var notifyOnce sync.Once
		err := c.Server(ctx, func(addr net.Addr) {
			notifyOnce.Do(func() { listening <- addr })
		})
		if !errors.Is(err, ErrRestartRequested) || ctx.Err() != nil {
			return err
		}
		window.Dispatch(func() { window.SetHTML(RestartingHTML()) })
	}
}

// installQuitMenu adds the standard Quit item on platforms that show an
// application menu bar. A webview window starts without one, so the quit
// shortcut would otherwise do nothing; windows without a menu bar skip it.
func installQuitMenu(window Window) {
	menu, ok := window.(QuitMenu)
	if !ok {
		return
	}
	menu.InstallQuitMenu(ApplicationName)
}

// bindFolderPicker exposes the native folder chooser to the page when the
// window supports one. Pages detect availability by the presence of the
// binding, so a window without a chooser simply leaves it undefined.
func bindFolderPicker(window Window) {
	picker, ok := window.(FolderPicker)
	if !ok {
		return
	}
	_ = window.Bind(FolderPickerBinding, func(initial string) (FolderSelection, error) {
		return picker.PickFolder(initial)
	})
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
