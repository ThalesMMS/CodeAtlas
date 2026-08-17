//go:build !desktop || (!windows && !darwin)

package desktop

func DefaultEnabled() bool { return false }
