package desktop

import (
	"context"
	"net"
)

type SizeHint int

const (
	SizeDefault SizeHint = iota
	SizeMinimum
)

type Window interface {
	SetTitle(string)
	SetSize(width, height int, hint SizeHint)
	Navigate(string)
	SetHTML(string)
	Dispatch(func())
	Run()
	Terminate()
	Destroy()
}

type WindowFactory interface {
	New() (Window, error)
	ShowFatal(title, message string)
}

type ServerFunc func(context.Context, func(net.Addr)) error
