package render

import (
	"strings"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

func TestFormatCountdown(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "now"},
		{0, "now"},
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{3*time.Hour + 12*time.Minute, "3h 12m"},
		{2 * time.Hour, "2h"},
		{5*24*time.Hour + 4*time.Hour, "5d 4h"},
		{3 * 24 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := FormatCountdown(c.d); got != c.want {
			t.Errorf("FormatCountdown(%v): got %q, want %q", c.d, got, c.want)
		}
	}
}

func healthy(provider, plan string, now time.Time) model.ProviderResult {
	return model.Healthy(model.Snapshot{
		Provider: provider,
		Plan:     plan,
		Session:  model.Window{Present: true, UsedPercent: 28, ResetsAt: now.Add(3*time.Hour + 12*time.Minute)},
		Weekly:   model.Window{Present: true, UsedPercent: 59, ResetsAt: now.Add(5*24*time.Hour + 4*time.Hour)},
		Source:   "test",
	})
}

func TestRenderWideIsSideBySide(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{
		healthy("claude", "Max", now),
		healthy("codex", "Pro", now),
	}, 120, false, now, now)

	first := strings.SplitN(out, "\n", 2)[0]
	if got := strings.Count(first, "╭"); got != 2 {
		t.Errorf("wide layout first row should have 2 card tops, got %d: %q", got, first)
	}
}

func TestRenderNarrowIsStacked(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{
		healthy("claude", "Max", now),
		healthy("codex", "Pro", now),
	}, 30, false, now, now)

	first := strings.SplitN(out, "\n", 2)[0]
	if got := strings.Count(first, "╭"); got != 1 {
		t.Errorf("narrow layout first row should have 1 card top, got %d: %q", got, first)
	}
}

func TestRenderNoColor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{healthy("claude", "Max", now)}, 120, false, now, now)
	if strings.Contains(out, "\x1b") {
		t.Errorf("NO_COLOR render must not contain ANSI escapes: %q", out)
	}
}

func TestRenderColorHasEscapes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{healthy("claude", "Max", now)}, 120, true, now, now)
	if !strings.Contains(out, "\x1b") {
		t.Error("color render should contain ANSI escapes")
	}
}

func TestRenderOneProviderFailed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{
		healthy("claude", "Max", now),
		model.Failed("codex", model.FailureNotSignedIn),
	}, 120, false, now, now)

	if !strings.Contains(out, "Not signed in") {
		t.Errorf("failed card must show safe status; output:\n%s", out)
	}
	// The healthy card must still render.
	if !strings.Contains(out, "Session") {
		t.Errorf("healthy card must still render alongside failed one:\n%s", out)
	}
}

func TestRenderFreshnessFooter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	saved := now.Add(-15 * time.Second)
	out := Render([]model.ProviderResult{healthy("claude", "Max", now)}, 120, false, now, saved)
	if !strings.Contains(out, "updated 15s ago") {
		t.Errorf("expected freshness footer 'updated 15s ago':\n%s", out)
	}
}

func TestRenderFreshnessFooterJustNow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{healthy("claude", "Max", now)}, 120, false, now, now)
	if !strings.Contains(out, "updated just now") {
		t.Errorf("expected 'updated just now' for a fresh save:\n%s", out)
	}
	if strings.Contains(out, "now ago") {
		t.Errorf("footer should not read 'now ago':\n%s", out)
	}
}

func TestCardFixedShape(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lines := Card(healthy("claude", "Max", now), now, false)
	if len(lines) != cardHeight {
		t.Fatalf("card should have %d lines, got %d", cardHeight, len(lines))
	}
	for i, ln := range lines {
		if got := visibleLen(ln); got != cardWidth {
			t.Errorf("line %d visible width = %d, want %d: %q", i, got, cardWidth, ln)
		}
	}
}

func TestCardAbsentWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := model.Healthy(model.Snapshot{
		Provider: "codex",
		Session:  model.Window{Present: true, UsedPercent: 10, ResetsAt: now.Add(time.Hour)},
		Weekly:   model.Window{Present: false},
	})
	out := strings.Join(Card(r, now, false), "\n")
	if !strings.Contains(out, "n/a") {
		t.Errorf("absent weekly window should render n/a:\n%s", out)
	}
}
