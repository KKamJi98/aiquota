// Package provider defines the read-only contract that every AI subscription
// quota adapter implements. Concrete adapters live in provider subpackages
// (codex, claude) and are the only code that touches native CLIs.
package provider

import (
	"context"

	"github.com/kkamji98/aiquota/internal/model"
)

// Provider queries one AI subscription's quota in a strictly read-only manner.
//
// Auth boundary (non-negotiable). Implementations MUST invoke only installed
// native CLIs in read-only status/query modes and consume their stdout/stderr.
// Implementations MUST NOT read, write, refresh, print, cache, or transmit
// OAuth credentials, browser cookies, Keychain values, API keys, or credential
// files, and MUST NOT trigger login, logout, token refresh, or quota reset side
// effects, nor open a browser.
//
// Failure contract. On any failure, Fetch MUST return a *model.ProviderError
// carrying a safe category. Raw child-process output MUST NOT appear in the
// returned error, the returned Snapshot, or anywhere persisted. A failure from
// one Provider must never prevent another Provider's result from rendering, so
// callers treat each Fetch independently.
type Provider interface {
	// Name is the stable provider identifier, e.g. "claude" or "codex".
	Name() string
	// Fetch returns a normalized Snapshot on success, or a *model.ProviderError
	// on failure. It must honor ctx cancellation and deadlines.
	Fetch(ctx context.Context) (model.Snapshot, error)
}
