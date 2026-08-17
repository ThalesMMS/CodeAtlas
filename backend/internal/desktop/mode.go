package desktop

import (
	"fmt"
	"strconv"
	"strings"
)

type Mode struct {
	Enabled bool
	Args    []string
}

func ParseMode(args []string, defaultEnabled bool) (Mode, error) {
	mode := Mode{Enabled: defaultEnabled, Args: make([]string, 0, len(args))}
	parseFlags := true
	for _, arg := range args {
		if !parseFlags {
			mode.Args = append(mode.Args, arg)
			continue
		}
		if arg == "--" {
			parseFlags = false
			mode.Args = append(mode.Args, arg)
			continue
		}
		if arg == "-desktop" || arg == "--desktop" {
			mode.Enabled = true
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if hasValue && (name == "-desktop" || name == "--desktop") {
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return mode, fmt.Errorf("invalid -desktop value: %w", err)
			}
			mode.Enabled = enabled
			continue
		}
		mode.Args = append(mode.Args, arg)
	}
	return mode, nil
}
