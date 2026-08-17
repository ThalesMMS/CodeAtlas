//go:build (!windows && !darwin) || (darwin && !cgo)

package desktop

import (
	"fmt"
	"os"
)

func showFatalDialog(title, message string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", safeFatalText(title), safeFatalText(message))
}
