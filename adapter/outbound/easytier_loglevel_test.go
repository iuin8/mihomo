//go:build !no_easytier

package outbound

import (
	"testing"

	corehost "github.com/easytier/easytier/easytier-go"
)

// The node-level log-level is the only thing that can raise the guest logger:
// the WASI guest starts at INFO and the TOML logging section is not read by the
// embedded core. Keep the string→level mapping pinned so a typo fails fast
// instead of silently logging at the default level.
func TestParseEasyTierLogLevel(t *testing.T) {
	valid := map[string]corehost.LogLevel{
		"":        corehost.LogLevelInfo,
		"off":     corehost.LogLevelOff,
		"error":   corehost.LogLevelError,
		"warn":    corehost.LogLevelWarn,
		"warning": corehost.LogLevelWarn,
		"info":    corehost.LogLevelInfo,
		"debug":   corehost.LogLevelDebug,
		"trace":   corehost.LogLevelTrace,
		"DEBUG":   corehost.LogLevelDebug,
	}
	for in, want := range valid {
		got, err := parseEasyTierLogLevel(in)
		if err != nil {
			t.Fatalf("parseEasyTierLogLevel(%q): unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("parseEasyTierLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"verbose", "dbg", "warning2", "disabled"} {
		if _, err := parseEasyTierLogLevel(in); err == nil {
			t.Fatalf("parseEasyTierLogLevel(%q): expected an error", in)
		}
	}
}
