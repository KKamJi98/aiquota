// Package render turns normalized provider results into ANSI terminal cards.
// It knows nothing about JSON-RPC, terminal parsing, or transports; it consumes
// only model types. Width, color, and the current time are injected by the
// caller so rendering is fully deterministic and testable.
package render

import "strings"

// ANSI SGR codes. Kept minimal; applied only when color is enabled.
const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiCyan   = "\x1b[36m"
)

// colorize wraps s in code..reset when enabled, otherwise returns s unchanged.
func colorize(s, code string, enabled bool) string {
	if !enabled || code == "" {
		return s
	}
	return code + s + ansiReset
}

// remainingColor picks a color code by how much of a window is left.
func remainingColor(remaining int) string {
	switch {
	case remaining >= 50:
		return ansiGreen
	case remaining >= 20:
		return ansiYellow
	default:
		return ansiRed
	}
}

// progressBar renders a bar of the given cell width reflecting remaining percent.
// The bar characters are colored (when enabled); the returned string always has
// exactly `width` visible cells so card alignment is preserved.
func progressBar(remaining, width int, enabled bool) string {
	if width <= 0 {
		return ""
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	filled := (remaining*width + 50) / 100 // round to nearest cell
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return colorize(bar, remainingColor(remaining), enabled)
}
