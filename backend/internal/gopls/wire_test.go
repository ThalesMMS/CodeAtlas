package gopls

import (
	"github.com/ThalesMMS/CodeAtlas/internal/lspadapter"
	"github.com/ThalesMMS/CodeAtlas/internal/lspfacts"
)

// Test aliases keep fixtures terse while production uses lspfacts directly.
type lspPosition = lspfacts.Position
type lspRange = lspfacts.Range
type lspLocation = lspfacts.Location
type callHierarchyItem = lspfacts.CallHierarchyItem
type incomingCall = lspfacts.IncomingCall
type outgoingCall = lspfacts.OutgoingCall
type publishDiagnosticsParams = lspfacts.PublishDiagnosticsParams
type lspDiagnostic = lspfacts.Diagnostic

const maxHoverBytes = lspfacts.MaxHoverBytes

func boundString(value string) string { return lspfacts.BoundString(value) }
func isExternalPath(path string) bool { return lspadapter.IsExternalPath(path) }
