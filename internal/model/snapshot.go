// Package model defines the normalized, provider-agnostic quota types that flow
// from provider adapters to the renderer. Adapters translate their native source
// (Codex JSON-RPC, Claude terminal output) into these types; the renderer knows
// only about these types and never about any transport or parsing detail.
package model

import "time"

// Window is one quota window, e.g. the 5-hour session window or the 1-week
// weekly window. UsedPercent and ResetsAt must come from explicit provider
// values, never guessed from local token logs.
type Window struct {
	// Present reports whether the provider actually returned this window.
	// A false value means "provider has no such window", which the renderer
	// shows distinctly from a zero-usage window.
	Present bool
	// UsedPercent is the integer percent of the window consumed, 0-100.
	UsedPercent int
	// ResetsAt is when the window resets to full.
	ResetsAt time.Time
}

// RemainingPercent returns the percent of the window left, clamped to 0-100.
func (w Window) RemainingPercent() int {
	r := 100 - w.UsedPercent
	switch {
	case r < 0:
		return 0
	case r > 100:
		return 100
	default:
		return r
	}
}

// Snapshot is the normalized quota view for a single provider that the renderer
// consumes and the cache persists. It carries only non-sensitive fields.
type Snapshot struct {
	Provider  string
	Plan      string
	Session   Window
	Weekly    Window
	UpdatedAt time.Time
	Source    string
}

// FailureCategory is a safe, non-sensitive classification of a provider failure.
// It is the only failure detail permitted to reach output, cache, or debug logs;
// raw child-process text and credentials must never be surfaced.
type FailureCategory string

const (
	FailureNone        FailureCategory = ""
	FailureNotSignedIn FailureCategory = "not_signed_in"
	FailureTimedOut    FailureCategory = "timed_out"
	FailureUnsupported FailureCategory = "unsupported"
	FailureUnavailable FailureCategory = "unavailable"
)

// Label returns the short, safe status string shown on a failed card.
func (c FailureCategory) Label() string {
	switch c {
	case FailureNotSignedIn:
		return "Not signed in"
	case FailureTimedOut:
		return "Timed out"
	case FailureUnsupported:
		return "Unsupported quota response"
	default:
		return "Unavailable"
	}
}

// ProviderError is the only error type provider adapters may return. It carries
// a safe category and deliberately holds no raw message, so no child-process
// output, credential, or account detail can leak through the error path.
type ProviderError struct {
	Category FailureCategory
}

func (e *ProviderError) Error() string { return string(e.Category) }

// NewProviderError builds a ProviderError with the given safe category.
func NewProviderError(c FailureCategory) *ProviderError {
	return &ProviderError{Category: c}
}
