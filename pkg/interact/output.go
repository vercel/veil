package interact

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/term"
)

const (
	OutputPretty = "pretty"
	OutputJSON   = "json"
)

// outputFormat holds the global output format set by --output.
var outputFormat atomic.Pointer[string]

// SetOutputFormat sets the global output format. Typically called from the
// root Before hook after parsing --output.
func SetOutputFormat(format string) {
	outputFormat.Store(&format)
}

// GetOutputFormat returns the current output format. If SetOutputFormat has
// not been called, it defaults to "json" for agents and "pretty" for humans.
func GetOutputFormat() string {
	if v := outputFormat.Load(); v != nil && *v != "" {
		return *v
	}
	if isAgent() {
		return OutputJSON
	}
	return OutputPretty
}

// IsJSON returns true when the output format is JSON.
func IsJSON() bool {
	return GetOutputFormat() == OutputJSON
}

// isAgent returns true when the command is being driven by an automated agent
// rather than a human. It checks whether CI=true or stdin is not a terminal.
var isAgent = sync.OnceValue(func() bool {
	if strings.EqualFold(os.Getenv("CI"), "true") {
		return true
	}
	return !term.IsTerminal(int(os.Stdin.Fd()))
})
