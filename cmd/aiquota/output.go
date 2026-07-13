package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

// jsonWindow is the safe, machine-readable view of one quota window.
type jsonWindow struct {
	Present      bool       `json:"present"`
	RemainingPct int        `json:"remaining_percent"`
	ResetsAt     *time.Time `json:"resets_at,omitempty"`
}

// jsonProvider is the safe per-provider JSON payload: only normalized,
// non-sensitive fields, plus a failure category when unhealthy.
type jsonProvider struct {
	Provider  string      `json:"provider"`
	Plan      string      `json:"plan,omitempty"`
	OK        bool        `json:"ok"`
	Failure   string      `json:"failure,omitempty"`
	Session   *jsonWindow `json:"session,omitempty"`
	Weekly    *jsonWindow `json:"weekly,omitempty"`
	Source    string      `json:"source,omitempty"`
	UpdatedAt *time.Time  `json:"updated_at,omitempty"`
}

func toJSONWindow(w model.Window) *jsonWindow {
	if !w.Present {
		return nil // omitted from JSON (e.g. Codex has no 5h session window)
	}
	jw := &jsonWindow{Present: true, RemainingPct: w.RemainingPercent()}
	if !w.ResetsAt.IsZero() {
		t := w.ResetsAt
		jw.ResetsAt = &t
	}
	return jw
}

func printJSON(results []model.ProviderResult, savedAt time.Time) {
	out := struct {
		SavedAt   time.Time      `json:"saved_at"`
		Providers []jsonProvider `json:"providers"`
	}{SavedAt: savedAt}

	for _, r := range results {
		jp := jsonProvider{
			Provider: r.Snapshot.Provider,
			Plan:     r.Snapshot.Plan,
			OK:       r.OK(),
			Source:   r.Snapshot.Source,
		}
		if !r.OK() {
			jp.Failure = string(r.Failure)
		} else {
			jp.Session = toJSONWindow(r.Snapshot.Session)
			jp.Weekly = toJSONWindow(r.Snapshot.Weekly)
			if !r.Snapshot.UpdatedAt.IsZero() {
				t := r.Snapshot.UpdatedAt
				jp.UpdatedAt = &t
			}
		}
		out.Providers = append(out.Providers, jp)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "aiquota: json encode failed")
		os.Exit(1)
	}
}
