// Package integration holds opt-in tests that exercise the real installed native
// CLIs. They are skipped unless AIQUOTA_INTEGRATION=1, so `go test ./...` and CI
// never spawn provider processes or depend on a logged-in account. These probes
// are strictly read-only: they never trigger login, logout, refresh, or reset.
package integration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
	"github.com/kkamji98/aiquota/internal/provider"
	"github.com/kkamji98/aiquota/internal/provider/claude"
	"github.com/kkamji98/aiquota/internal/provider/codex"
)

func requireOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("AIQUOTA_INTEGRATION") != "1" {
		t.Skip("set AIQUOTA_INTEGRATION=1 to run real-account integration probes")
	}
}

// checkProvider asserts that a Fetch either succeeds with sane, present windows
// or fails with a safe *model.ProviderError category. It never logs raw output,
// credentials, or account identifiers.
func checkProvider(t *testing.T, p provider.Provider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snap, err := p.Fetch(ctx)
	if err != nil {
		var pe *model.ProviderError
		if !errors.As(err, &pe) {
			t.Fatalf("%s: error must be a *model.ProviderError, got %T", p.Name(), err)
		}
		t.Logf("%s: safe failure category=%q (acceptable)", p.Name(), pe.Category)
		return
	}
	if snap.Provider != p.Name() {
		t.Errorf("%s: snapshot.Provider=%q, want %q", p.Name(), snap.Provider, p.Name())
	}
	if !snap.Session.Present && !snap.Weekly.Present {
		t.Errorf("%s: healthy snapshot has no present windows", p.Name())
	}
	for label, w := range map[string]model.Window{"session": snap.Session, "weekly": snap.Weekly} {
		if w.Present && (w.RemainingPercent() < 0 || w.RemainingPercent() > 100) {
			t.Errorf("%s %s: remaining %d out of range", p.Name(), label, w.RemainingPercent())
		}
	}
	t.Logf("%s: ok session_present=%v weekly_present=%v", p.Name(), snap.Session.Present, snap.Weekly.Present)
}

func TestCodexIntegration(t *testing.T) {
	requireOptIn(t)
	checkProvider(t, codex.New())
}

func TestClaudeIntegration(t *testing.T) {
	requireOptIn(t)
	checkProvider(t, claude.New())
}
