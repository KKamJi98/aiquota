package claude

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

// parseUsage turns the raw terminal capture of the Claude CLI `/usage` view into
// a normalized Snapshot. It is pure: it takes only text and returns only safe,
// non-sensitive values, so it can be exercised by fixture tests with no PTY or
// child process. On any failure it returns a *model.ProviderError carrying a
// safe category and never surfaces raw child output.
func parseUsage(raw string) (model.Snapshot, error) {
	return parseUsageAt(raw, time.Now())
}

// Escape sequences the CLI emits around the /usage view. Stripped before any
// text matching so the parser reads only human-visible characters.
var (
	reCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	reOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	reEsc = regexp.MustCompile(`\x1b[()#][0-9A-Za-z]|\x1b[=>NODEHM78]`)

	rePct   = regexp.MustCompile(`(\d+)%\s*used`)
	reReset = regexp.MustCompile(`(?i)resets\s+(.+?)\s*\(([A-Za-z]+(?:/[A-Za-z_]+)*)\)`)
)

// signedOutMarkers are phrases the CLI shows when there is no usable subscription
// session. They are matched case-insensitively against the sanitized capture.
// NOTE: these are best-effort. The healthy and unparseable paths are verified
// against real captures; the exact signed-out wording is not (forcing a real
// logout would touch credentials, which the auth boundary forbids).
var signedOutMarkers = []string{
	"sign in to claude",
	"please sign in",
	"log in to claude",
	"please log in",
	"run /login",
	"claude login",
	"not signed in",
	"not authenticated",
}

// parseUsageAt is parseUsage with an injectable clock so reset times, which the
// CLI prints without a full date, resolve deterministically in tests.
func parseUsageAt(raw string, now time.Time) (model.Snapshot, error) {
	text := sanitize(raw)
	lower := strings.ToLower(text)

	for _, m := range signedOutMarkers {
		if strings.Contains(lower, m) {
			return model.Snapshot{}, model.NewProviderError(model.FailureNotSignedIn)
		}
	}

	session, sOK := parseWindow(text, "current session", now)
	weekly, wOK := parseWindow(text, "current week", now)
	if !sOK || !wOK {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnsupported)
	}

	return model.Snapshot{
		Provider:  "claude",
		Session:   session,
		Weekly:    weekly,
		UpdatedAt: now,
		Source:    "claude /usage",
	}, nil
}

// parseWindow locates the labeled window (e.g. "current session", "current
// week") in the sanitized text and reads its percent-used and reset time from
// the text that follows, bounded so it never bleeds into the next window.
func parseWindow(text, label string, now time.Time) (model.Window, bool) {
	lower := strings.ToLower(text)
	start := strings.Index(lower, label)
	if start < 0 {
		return model.Window{}, false
	}
	segment := text[start:]
	// Bound the segment at the start of the OTHER window so, e.g., the session
	// block never captures the weekly block's numbers.
	if end := nextWindowIndex(strings.ToLower(segment)); end > 0 {
		segment = segment[:end]
	}

	pm := rePct.FindStringSubmatch(segment)
	rm := reReset.FindStringSubmatch(segment)
	if pm == nil || rm == nil {
		return model.Window{}, false
	}
	pct, err := strconv.Atoi(pm[1])
	if err != nil || pct < 0 || pct > 100 {
		return model.Window{}, false
	}
	reset, err := parseReset(rm[1], rm[2], now)
	if err != nil {
		return model.Window{}, false
	}
	return model.Window{Present: true, UsedPercent: pct, ResetsAt: reset}, true
}

// nextWindowIndex returns the offset of the next window label after position 0,
// or -1 if none. Used to bound one window's text so it cannot read another's.
func nextWindowIndex(lowerSegment string) int {
	best := -1
	for _, label := range []string{"current session", "current week"} {
		// Skip the label at position 0 (the current window's own label).
		if i := strings.Index(lowerSegment[1:], label); i >= 0 {
			if best < 0 || i+1 < best {
				best = i + 1
			}
		}
	}
	return best
}

// parseReset resolves the CLI's abbreviated reset string into an absolute time.
// The CLI prints either a bare clock time ("11:40pm") for the short session
// window or a month/day plus time ("Jul 18 at 5pm") for the weekly window, each
// followed by an IANA zone the caller has already extracted.
func parseReset(core, zone string, now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = now.Location()
	}
	core = strings.TrimSpace(core)

	if datePart, timePart, ok := strings.Cut(core, " at "); ok {
		h, m, err := parseClock(strings.TrimSpace(timePart))
		if err != nil {
			return time.Time{}, err
		}
		d, err := time.Parse("Jan 2", strings.TrimSpace(datePart))
		if err != nil {
			return time.Time{}, err
		}
		year := now.In(loc).Year()
		t := time.Date(year, d.Month(), d.Day(), h, m, 0, 0, loc)
		// The weekly reset is within the coming week; if computing with the
		// current year lands it well in the past, the year has rolled over.
		if t.Before(now.Add(-24 * time.Hour)) {
			t = time.Date(year+1, d.Month(), d.Day(), h, m, 0, 0, loc)
		}
		return t, nil
	}

	h, m, err := parseClock(core)
	if err != nil {
		return time.Time{}, err
	}
	n := now.In(loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, loc)
	// A bare clock time is the next occurrence of that time.
	if t.Before(n) {
		t = t.Add(24 * time.Hour)
	}
	return t, nil
}

// parseClock reads a 12-hour clock token as printed by the CLI, with or without
// minutes ("5pm", "11:40pm"), returning hour (0-23) and minute.
func parseClock(s string) (int, int, error) {
	for _, layout := range []string{"3:04pm", "3pm"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Hour(), t.Minute(), nil
		}
	}
	return 0, 0, fmt.Errorf("unrecognized clock %q", s)
}

// sanitize removes terminal escape sequences and decorative glyphs (progress-bar
// blocks, box drawing) and collapses whitespace, leaving flat readable text.
func sanitize(raw string) string {
	s := reOSC.ReplaceAllString(raw, "")
	s = reCSI.ReplaceAllString(s, "")
	s = reEsc.ReplaceAllString(s, "")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 0x2500 && r <= 0x259F: // box drawing + block elements
			b.WriteByte(' ')
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f: // other control chars
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
