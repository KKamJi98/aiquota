package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

const (
	innerWidth = 32 // columns between the card's vertical borders
	cardWidth  = innerWidth + 2
	cardHeight = 9 // fixed so side-by-side cards align row for row
	barWidth   = 10
	labelWidth = 8
	gap        = "  " // spacing between side-by-side cards
)

// FormatCountdown renders a positive duration as a compact two-unit string,
// e.g. "45s", "12m", "3h 12m", "5d 4h". Non-positive durations render as "now".
func FormatCountdown(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) - days*24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	}
}

// visibleLen counts display cells, ignoring ANSI SGR escape sequences.
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		default:
			n++
		}
	}
	return n
}

// padRight pads s with spaces to n visible cells (no-op if already >= n).
func padRight(s string, n int) string {
	if diff := n - visibleLen(s); diff > 0 {
		return s + strings.Repeat(" ", diff)
	}
	return s
}

// resetLabel describes when a window resets, relative to now.
func resetLabel(w model.Window, now time.Time) string {
	if w.ResetsAt.IsZero() {
		return "resets n/a"
	}
	return "resets in " + FormatCountdown(w.ResetsAt.Sub(now))
}

// windowLines renders the two body lines for one present window: a bar+percent
// line and a reset line. Callers skip absent windows, which are not shown at all.
func windowLines(label string, w model.Window, now time.Time, color bool) []string {
	name := padRight(label, labelWidth)
	rem := w.RemainingPercent()
	bar := progressBar(rem, barWidth, color)
	pct := fmt.Sprintf("%3d%%", rem)
	first := "  " + name + bar + " " + pct + " left"
	second := "    " + colorize(resetLabel(w, now), ansiDim, color)
	return []string{first, second}
}

// Card renders one provider result as exactly cardHeight lines, each cardWidth
// cells wide, so cards stack or sit side by side without misalignment.
func Card(r model.ProviderResult, now time.Time, color bool) []string {
	top := "╭" + strings.Repeat("─", innerWidth) + "╮"
	bottom := "╰" + strings.Repeat("─", innerWidth) + "╯"

	name := capitalize(r.Snapshot.Provider)
	if name == "" {
		name = "Unknown"
	}

	var body []string
	if r.OK() {
		header := "  " + colorize(name, ansiBold, color)
		if r.Snapshot.Plan != "" {
			// right-align the plan within the inner width
			left := "  " + name
			padded := left + strings.Repeat(" ", max(1, innerWidth-2-visibleLen(name)-visibleLen(r.Snapshot.Plan))) + r.Snapshot.Plan
			header = colorizeHeader(padded, name, r.Snapshot.Plan, color)
		}
		body = append(body, header, "")
		// Only present windows are shown; a provider may expose just one (e.g.
		// Codex now reports a weekly limit and no 5h session window).
		for _, w := range []struct {
			label  string
			window model.Window
		}{
			{"Session", r.Snapshot.Session},
			{"Weekly", r.Snapshot.Weekly},
		} {
			if !w.window.Present {
				continue
			}
			body = append(body, windowLines(w.label, w.window, now, color)...)
			body = append(body, "")
		}
	} else {
		header := "  " + colorize(name, ansiBold, color)
		status := "  " + colorize(r.Failure.Label(), ansiRed, color)
		body = append(body, header, "", status)
	}

	// Assemble: top, body padded/truncated to cardHeight-2 rows, bottom.
	lines := make([]string, 0, cardHeight)
	lines = append(lines, top)
	inner := cardHeight - 2
	for i := 0; i < inner; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		lines = append(lines, "│"+padRight(content, innerWidth)+"│")
	}
	lines = append(lines, bottom)
	return lines
}

// colorizeHeader applies bold to the provider name and dim to the plan without
// disturbing the pre-computed padding between them.
func colorizeHeader(padded, name, plan string, color bool) string {
	if !color {
		return padded
	}
	padded = strings.Replace(padded, name, colorize(name, ansiBold, color), 1)
	padded = strings.Replace(padded, plan, colorize(plan, ansiDim, color), 1)
	return padded
}

// Render lays out all provider cards for the given terminal width and returns
// the full string to print. now/savedAt drive the freshness footer.
func Render(results []model.ProviderResult, termWidth int, color bool, now, savedAt time.Time) string {
	cards := make([][]string, 0, len(results))
	for _, r := range results {
		cards = append(cards, Card(r, now, color))
	}

	var b strings.Builder
	sideBySide := len(cards) >= 2 && termWidth >= len(cards)*cardWidth+(len(cards)-1)*len(gap)
	if sideBySide {
		for row := 0; row < cardHeight; row++ {
			parts := make([]string, len(cards))
			for c := range cards {
				parts[c] = cards[c][row]
			}
			b.WriteString(strings.Join(parts, gap))
			b.WriteString("\n")
		}
	} else {
		for i, card := range cards {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(strings.Join(card, "\n"))
			b.WriteString("\n")
		}
	}

	if !savedAt.IsZero() {
		footer := "updated just now"
		if age := now.Sub(savedAt); age >= time.Second {
			footer = "updated " + FormatCountdown(age) + " ago"
		}
		b.WriteString(colorize(footer, ansiDim, color))
		b.WriteString("\n")
	}
	return b.String()
}

// capitalize upper-cases the first rune of s (ASCII provider ids only).
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
