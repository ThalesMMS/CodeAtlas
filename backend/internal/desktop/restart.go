package desktop

import "errors"

// ErrRestartRequested is returned by a ServerFunc when the running CodeAtlas
// composition asked to be torn down and started again with the latest saved
// settings (for example after the workspace changed). Mode runners treat it as
// a request to run the server again in the same process rather than as a
// failure.
var ErrRestartRequested = errors.New("codeatlas restart requested")
