package claude

import (
	"errors"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

// All fixtures below are MANUALLY AUTHORED synthetic text approximating the
// sanitized shape of a real `/usage` capture. No real terminal output, account
// identifier, or credential appears here.

// healthyFixture mirrors the two-window layout the CLI renders, including the
// ANSI reset codes and block-bar glyphs that sanitize() must strip.
const healthyFixture = "\x1b[38;5;153mSettings  Status  Config  Usage  Stats\x1b[39m\n" +
	"Current session\n" +
	"\x1b[38;5;114m████████▌\x1b[39m 55% used   Resets 11:40pm (Asia/Seoul)\n" +
	"Current week (all models)\n" +
	"\x1b[38;5;114m████▌\x1b[39m 33% used   Resets Jul 18 at 5pm (Asia/Seoul)\n" +
	"What's contributing to your limits usage?\n"

func TestParseUsageHealthy(t *testing.T) {
	// A fixed clock so the CLI's date-less reset strings resolve deterministically.
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, time.July, 12, 15, 0, 0, 0, seoul)

	snap, err := parseUsageAt(healthyFixture, now)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if snap.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", snap.Provider, "claude")
	}
	if !snap.Session.Present || snap.Session.UsedPercent != 55 {
		t.Errorf("Session = %+v, want present with 55%%", snap.Session)
	}
	if !snap.Weekly.Present || snap.Weekly.UsedPercent != 33 {
		t.Errorf("Weekly = %+v, want present with 33%%", snap.Weekly)
	}

	// Session resets today at 23:40 Seoul (later than the 15:00 clock).
	wantSession := time.Date(2026, time.July, 12, 23, 40, 0, 0, seoul)
	if !snap.Session.ResetsAt.Equal(wantSession) {
		t.Errorf("Session.ResetsAt = %v, want %v", snap.Session.ResetsAt, wantSession)
	}
	// Weekly resets Jul 18 at 17:00 Seoul, current year.
	wantWeekly := time.Date(2026, time.July, 18, 17, 0, 0, 0, seoul)
	if !snap.Weekly.ResetsAt.Equal(wantWeekly) {
		t.Errorf("Weekly.ResetsAt = %v, want %v", snap.Weekly.ResetsAt, wantWeekly)
	}
}

func TestParseUsageBareClockRollsToNextDay(t *testing.T) {
	// When the bare session clock time has already passed today, it must resolve
	// to tomorrow's occurrence.
	seoul, _ := time.LoadLocation("Asia/Seoul")
	now := time.Date(2026, time.July, 12, 23, 50, 0, 0, seoul) // after 11:40pm
	snap, err := parseUsageAt(healthyFixture, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, time.July, 13, 23, 40, 0, 0, seoul)
	if !snap.Session.ResetsAt.Equal(want) {
		t.Errorf("Session.ResetsAt = %v, want %v (next day)", snap.Session.ResetsAt, want)
	}
}

func TestParseUsageNotSignedIn(t *testing.T) {
	// Synthetic: exact signed-out wording is unverified (see signedOutMarkers).
	const fixture = "Welcome to Claude Code\nPlease sign in to Claude to continue.\nRun /login to authenticate.\n"
	_, err := parseUsageAt(fixture, time.Now())
	assertCategory(t, err, model.FailureNotSignedIn)
}

func TestParseUsageUnparseable(t *testing.T) {
	// A capture with no recognizable quota windows must be reported as
	// unsupported, never guessed.
	const fixture = "\x1b[2J\x1b[HClaude Code v2.1.207\nTips for getting started\nRun /init to create a CLAUDE.md\n"
	_, err := parseUsageAt(fixture, time.Now())
	assertCategory(t, err, model.FailureUnsupported)
}

func TestParseUsageOnlyOneWindowIsUnsupported(t *testing.T) {
	// One window present but not the other -> cannot honor the contract, so
	// unsupported rather than a half-filled snapshot.
	const fixture = "Current session\n██ 40% used   Resets 9:00am (Asia/Seoul)\n"
	_, err := parseUsageAt(fixture, time.Now())
	assertCategory(t, err, model.FailureUnsupported)
}

func assertCategory(t *testing.T, err error, want model.FailureCategory) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with category %q, got nil", want)
	}
	var pe *model.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *model.ProviderError, got %T: %v", err, err)
	}
	if pe.Category != want {
		t.Errorf("category = %q, want %q", pe.Category, want)
	}
}
