package model

import "testing"

func TestWindowRemainingPercent(t *testing.T) {
	cases := []struct {
		used, want int
	}{
		{0, 100},
		{28, 72},
		{100, 0},
		{-5, 100}, // clamp
		{130, 0},  // clamp
	}
	for _, c := range cases {
		got := Window{UsedPercent: c.used}.RemainingPercent()
		if got != c.want {
			t.Errorf("used=%d: got %d, want %d", c.used, got, c.want)
		}
	}
}

func TestFailureCategoryLabel(t *testing.T) {
	cases := map[FailureCategory]string{
		FailureNotSignedIn: "Not signed in",
		FailureTimedOut:    "Timed out",
		FailureUnsupported: "Unsupported quota response",
		FailureUnavailable: "Unavailable",
		FailureNone:        "Unavailable", // default arm
	}
	for c, want := range cases {
		if got := c.Label(); got != want {
			t.Errorf("%q: got %q, want %q", c, got, want)
		}
	}
}

func TestResultConstructors(t *testing.T) {
	if !Healthy(Snapshot{Provider: "codex"}).OK() {
		t.Error("Healthy result should be OK")
	}
	f := Failed("claude", FailureTimedOut)
	if f.OK() {
		t.Error("Failed result should not be OK")
	}
	if f.Snapshot.Provider != "claude" || f.Failure != FailureTimedOut {
		t.Errorf("Failed set wrong fields: %+v", f)
	}
}
