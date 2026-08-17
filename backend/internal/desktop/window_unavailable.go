//go:build !desktop || (!windows && !darwin)

package desktop

import "errors"

type unavailableFactory struct{}

func NativeFactory() WindowFactory { return unavailableFactory{} }

func (unavailableFactory) New() (Window, error) {
	return nil, errors.New("this CodeAtlas build does not include native desktop support")
}

func (unavailableFactory) ShowFatal(title, message string) {
	showFatalDialog(title, message)
}
