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
	}, 60, false, now, now)

	first := strings.SplitN(out, "\n", 2)[0]
	if got := strings.Count(first, "╭"); got != 1 {
		t.Errorf("narrow layout first row should have 1 card top, got %d: %q", got, first)
	}
}

func TestRenderVeryNarrowUsesCompactLayoutWithoutWrapping(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const width = 30
	out := Render([]model.ProviderResult{
		healthy("claude", "Max", now),
		healthy("codex", "Pro", now),
	}, width, false, now, now)

	if strings.ContainsAny(out, "╭╮╰╯│") {
		t.Fatalf("compact layout must not contain fixed-width card borders:\n%s", out)
	}
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if got := visibleLen(line); got > width {
			t.Errorf("line %d visible width = %d, want <= %d: %q", i, got, width, line)
		}
	}
	for _, want := range []string{"Claude Max", "Session 72% left", "Codex Pro", "updated just now"} {
		if !strings.Contains(out, want) {
			t.Errorf("compact layout missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTinyWidthTruncatesEveryCompactLine(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const width = 8
	out := Render([]model.ProviderResult{healthy("claude", "Max", now)}, width, true, now, now)

	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if got := visibleLen(line); got > width {
			t.Errorf("line %d visible width = %d, want <= %d: %q", i, got, width, line)
		}
	}
	if !strings.Contains(out, "\x1b") {
		t.Error("compact color render should contain ANSI escapes")
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

func TestCardAbsentWindowIsOmitted(t *testing.T) {
	// Codex-style: only a weekly window, no 5h session. The Session row must be
	// omitted entirely (not shown as "n/a"), while Weekly still renders.
	now := time.Unix(1_700_000_000, 0)
	r := model.Healthy(model.Snapshot{
		Provider: "codex",
		Session:  model.Window{Present: false},
		Weekly:   model.Window{Present: true, UsedPercent: 10, ResetsAt: now.Add(48 * time.Hour)},
	})
	out := strings.Join(Card(r, now, false), "\n")
	if strings.Contains(out, "Session") {
		t.Errorf("absent session window must not be shown:\n%s", out)
	}
	if strings.Contains(out, "n/a") {
		t.Errorf("absent window must be omitted, not rendered as n/a:\n%s", out)
	}
	if !strings.Contains(out, "Weekly") {
		t.Errorf("present weekly window must still render:\n%s", out)
	}
}

func TestCardTitleSpellsOutBrandIds(t *testing.T) {
	// The zai id is a brand, not a word: title-casing it to "Zai" misnames it.
	now := time.Unix(1_700_000_000, 0)
	out := strings.Join(Card(healthy("zai", "Coding Plan Pro", now), now, false), "\n")
	if !strings.Contains(out, "Z.AI") {
		t.Errorf("zai card should be titled Z.AI:\n%s", out)
	}
	if strings.Contains(out, "Zai") {
		t.Errorf("zai card must not be title-cased to Zai:\n%s", out)
	}
	if got := displayName("claude"); got != "Claude" {
		t.Errorf("other ids stay title-cased: got %q, want %q", got, "Claude")
	}
}

func TestCardPlanLabelIsCapitalized(t *testing.T) {
	// Providers report the tier verbatim ("team" from the Codex app-server), but
	// the card is a display surface: the plan reads as a proper label.
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		plan string
		want string
	}{
		{"team", "Team"},
		{"Coding Plan Pro", "Coding Plan Pro"},
	}
	for _, c := range cases {
		out := strings.Join(Card(healthy("codex", c.plan, now), now, false), "\n")
		if !strings.Contains(out, c.want) {
			t.Errorf("plan %q should render as %q:\n%s", c.plan, c.want, out)
		}
	}
}

func TestResetLabelForPassedWindow(t *testing.T) {
	// A reset time already in the past means the data predates the reset, not
	// that the window resets this instant.
	now := time.Unix(1_700_000_000, 0)
	w := model.Window{Present: true, UsedPercent: 91, ResetsAt: now.Add(-5 * time.Hour)}
	if got, want := resetLabel(w, now), "reset passed"; got != want {
		t.Errorf("resetLabel for a past reset: got %q, want %q", got, want)
	}
}

func TestRenderStaleProviderIsNamedInFooter(t *testing.T) {
	// A provider that failed this run keeps its last good snapshot, so the card
	// numbers can be hours old while the cache itself was just written.
	now := time.Unix(1_700_000_000, 0)
	fresh := healthy("claude", "Team", now)
	fresh.Snapshot.UpdatedAt = now
	stale := healthy("zai", "Coding Plan Pro", now)
	stale.Snapshot.UpdatedAt = now.Add(-6 * time.Hour)

	out := Render([]model.ProviderResult{fresh, stale}, 120, false, now, now)

	if !strings.Contains(out, "zai 6h old") {
		t.Errorf("footer should name the stale provider and its age:\n%s", out)
	}
	if strings.Contains(out, "claude 0s old") || strings.Contains(out, "claude ") {
		t.Errorf("a fresh provider must not appear in the stale footer:\n%s", out)
	}
}

func TestRenderUnknownUpdateTimeIsNotReportedStale(t *testing.T) {
	// A snapshot without UpdatedAt says nothing about its age; guessing "stale"
	// from a zero value would flag every provider that omits the field.
	now := time.Unix(1_700_000_000, 0)
	out := Render([]model.ProviderResult{healthy("claude", "Team", now)}, 120, false, now, now)
	if strings.Contains(out, "old") {
		t.Errorf("unknown update time must not be reported as stale:\n%s", out)
	}
}

func TestRenderStaleFooterNamesLastFailure(t *testing.T) {
	// When the cache recorded why a snapshot was carried over, the footer says
	// so next to the age, so "6d old" is never left unexplained.
	now := time.Unix(1_700_000_000, 0)
	stale := healthy("claude", "Team", now)
	stale.Snapshot.UpdatedAt = now.Add(-6 * time.Hour)
	stale.LastFailure = model.FailureUnsupported

	out := Render([]model.ProviderResult{stale}, 120, false, now, now)
	if !strings.Contains(out, "claude 6h old (unsupported)") {
		t.Errorf("footer should name the age and the last failure:\n%s", out)
	}
}

func TestCardExpiredWindowHidesPercent(t *testing.T) {
	// A window whose reset has passed describes a window that no longer exists,
	// so its percent must not be shown as if it were current.
	now := time.Unix(1_700_000_000, 0)
	r := model.Healthy(model.Snapshot{
		Provider: "claude",
		Session:  model.Window{Present: true, UsedPercent: 35, ResetsAt: now.Add(-6 * 24 * time.Hour)},
		Weekly:   model.Window{Present: true, UsedPercent: 55, ResetsAt: now.Add(2 * time.Hour)},
	})

	wide := strings.Join(Card(r, now, false), "\n")
	if strings.Contains(wide, "65% left") || !strings.Contains(wide, "Session stale") {
		t.Errorf("expired session must read stale, not a percent:\n%s", wide)
	}
	if !strings.Contains(wide, "reset passed") || !strings.Contains(wide, "45% left") {
		t.Errorf("the live weekly window must still render normally:\n%s", wide)
	}
	for i, line := range Card(r, now, false) {
		if got := visibleLen(line); got != cardWidth {
			t.Errorf("line %d width = %d, want %d: %q", i, got, cardWidth, line)
		}
	}

	compact := strings.Join(compactCard(r, now, false, 30), "\n")
	if strings.Contains(compact, "65% left") || !strings.Contains(compact, "Session stale") {
		t.Errorf("compact expired session must read stale:\n%s", compact)
	}
}

func BenchmarkRender(b *testing.B) {
	now := time.Unix(1_700_000_000, 0)
	results := []model.ProviderResult{
		healthy("claude", "Max", now),
		healthy("codex", "Pro", now),
	}
	b.ReportAllocs()
	for b.Loop() {
		Render(results, 120, false, now, now)
	}
}
