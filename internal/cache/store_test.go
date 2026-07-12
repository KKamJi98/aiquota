package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

func TestFreshness(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		age   time.Duration
		fresh bool
	}{
		{0, true},
		{10 * time.Second, true},
		{20 * time.Second, true},
		{21 * time.Second, false},
		{-time.Second, false}, // clock skew: saved in the future is not fresh
	}
	for _, c := range cases {
		d := Data{SavedAt: now.Add(-c.age)}
		if got := d.Fresh(now); got != c.fresh {
			t.Errorf("age=%v: Fresh=%v, want %v", c.age, got, c.fresh)
		}
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := &Store{Path: filepath.Join(t.TempDir(), "sub", "cache.json")}

	results := []model.ProviderResult{
		model.Healthy(model.Snapshot{Provider: "claude", Plan: "Max", Session: model.Window{Present: true, UsedPercent: 30}}),
		model.Failed("codex", model.FailureTimedOut),
	}
	if err := s.Save(results, now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := s.Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if !got.SavedAt.Equal(now) {
		t.Errorf("SavedAt: got %v, want %v", got.SavedAt, now)
	}
	if len(got.Results) != 2 || got.Results[0].Snapshot.Provider != "claude" || got.Results[1].Failure != model.FailureTimedOut {
		t.Errorf("roundtrip mismatch: %+v", got.Results)
	}
}

func TestLoadMissingIsMiss(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "nope.json")}
	_, ok, err := s.Load()
	if err != nil {
		t.Fatalf("missing cache should not error, got %v", err)
	}
	if ok {
		t.Error("missing cache should report ok=false")
	}
}

func TestMergeReplacesOnlySuccessful(t *testing.T) {
	prior := []model.ProviderResult{
		model.Healthy(model.Snapshot{Provider: "claude", Session: model.Window{Present: true, UsedPercent: 10}}),
		model.Healthy(model.Snapshot{Provider: "codex", Session: model.Window{Present: true, UsedPercent: 20}}),
	}
	fresh := []model.ProviderResult{
		model.Healthy(model.Snapshot{Provider: "claude", Session: model.Window{Present: true, UsedPercent: 55}}), // updated
		model.Failed("codex", model.FailureTimedOut),                                                             // failed this run
	}
	order := []string{"claude", "codex"}

	merged := Merge(prior, fresh, order)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged results, got %d", len(merged))
	}
	// claude succeeded -> replaced with fresh value.
	if merged[0].Snapshot.Provider != "claude" || merged[0].Snapshot.Session.UsedPercent != 55 {
		t.Errorf("claude should be updated to fresh: %+v", merged[0])
	}
	// codex failed -> keeps prior healthy value, not the fresh failure.
	if merged[1].Snapshot.Provider != "codex" || !merged[1].OK() || merged[1].Snapshot.Session.UsedPercent != 20 {
		t.Errorf("codex should retain prior healthy value: %+v", merged[1])
	}
}

func TestMergeFailedWithNoPriorRecordsFailure(t *testing.T) {
	fresh := []model.ProviderResult{model.Failed("codex", model.FailureNotSignedIn)}
	merged := Merge(nil, fresh, []string{"codex"})
	if len(merged) != 1 || merged[0].OK() || merged[0].Failure != model.FailureNotSignedIn {
		t.Errorf("failure with no prior should be recorded: %+v", merged)
	}
}
