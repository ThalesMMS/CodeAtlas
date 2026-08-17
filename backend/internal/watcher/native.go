package watcher

import (
	"sync"

	"github.com/fsnotify/fsnotify"
)

const (
	NativeCreate uint32 = uint32(fsnotify.Create)
	NativeWrite  uint32 = uint32(fsnotify.Write)
	NativeRemove uint32 = uint32(fsnotify.Remove)
	NativeRename uint32 = uint32(fsnotify.Rename)
	NativeChmod  uint32 = uint32(fsnotify.Chmod)
)

type NativeEvent struct {
	Name string
	Op   uint32
}

type NativeWatcher interface {
	Add(string) error
	Remove(string) error
	Close() error
	Events() <-chan NativeEvent
	Errors() <-chan error
}

type fsnotifyAdapter struct {
	watcher *fsnotify.Watcher
	events  chan NativeEvent
	errors  chan error
	once    sync.Once
	closing chan struct{}
	done    chan struct{}
}

func NewFsnotifyWatcher() (NativeWatcher, error) {
	inner, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, watcherError(ErrCodeCreateFailed, "failed to create fsnotify watcher", err)
	}
	adapter := &fsnotifyAdapter{
		watcher: inner,
		events:  make(chan NativeEvent, DefaultEventBufferSize),
		errors:  make(chan error, DefaultErrorBufferSize),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go adapter.bridge()
	return adapter, nil
}

func (a *fsnotifyAdapter) Add(path string) error    { return a.watcher.Add(path) }
func (a *fsnotifyAdapter) Remove(path string) error { return a.watcher.Remove(path) }
func (a *fsnotifyAdapter) Events() <-chan NativeEvent {
	return a.events
}
func (a *fsnotifyAdapter) Errors() <-chan error {
	return a.errors
}

func (a *fsnotifyAdapter) Close() error {
	var err error
	a.once.Do(func() {
		close(a.closing)
		err = a.watcher.Close()
		<-a.done
	})
	return err
}

func (a *fsnotifyAdapter) bridge() {
	defer close(a.done)
	defer close(a.events)
	defer close(a.errors)
	for {
		select {
		case <-a.closing:
			return
		case event, ok := <-a.watcher.Events:
			if !ok {
				return
			}
			select {
			case a.events <- NativeEvent{Name: event.Name, Op: uint32(event.Op)}:
			case <-a.closing:
				return
			}
		case err, ok := <-a.watcher.Errors:
			if !ok {
				return
			}
			select {
			case a.errors <- err:
			case <-a.closing:
				return
			}
		}
	}
}
