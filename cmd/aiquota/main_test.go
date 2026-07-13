package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/cache"
	"github.com/kkamji98/aiquota/internal/model"
	"github.com/kkamji98/aiquota/internal/provider"
)

type stubProvider struct {
	name  string
	fetch func(context.Context) (model.Snapshot, error)
}

func (p stubProvider) Name() string { return p.name }

func (p stubProvider) Fetch(ctx context.Context) (model.Snapshot, error) {
	return p.fetch(ctx)
}

func TestNormalizeInterval(t *testing.T) {
	cases := []struct {
		sec  int
		want time.Duration
	}{
		{0, 60 * time.Second},    // unset -> default
		{-5, 60 * time.Second},   // negative -> default
		{1, 2 * time.Second},     // below floor -> floor
		{2, 2 * time.Second},     // floor
		{3, 3 * time.Second},     // passthrough
		{300, 300 * time.Second}, // passthrough
	}
	for _, c := range cases {
		if got := normalizeInterval(c.sec); got != c.want {
			t.Errorf("normalizeInterval(%d) = %v, want %v", c.sec, got, c.want)
		}
	}
}

func TestRegisterFlagsShortAliases(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options cliOptions
	registerFlags(flags, &options)

	if err := flags.Parse([]string{"-r", "-j", "-d", "-n", "-w", "-i", "300"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !options.refresh || !options.jsonOut || !options.debug || !options.noColor || !options.watch {
		t.Errorf("short aliases did not enable all boolean options: %+v", options)
	}
	if options.interval != 300 {
		t.Errorf("-i interval = %d, want 300", options.interval)
	}
}

func TestRegisterFlagsLongOptions(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options cliOptions
	registerFlags(flags, &options)

	if err := flags.Parse([]string{"--refresh", "--json", "--debug", "--no-color", "--watch", "--interval", "300"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !options.refresh || !options.jsonOut || !options.debug || !options.noColor || !options.watch {
		t.Errorf("long options did not enable all boolean options: %+v", options)
	}
	if options.interval != 300 {
		t.Errorf("--interval = %d, want 300", options.interval)
	}
}

func TestWriteWatchFrameDoesNotClearBeforeFrame(t *testing.T) {
	var got bytes.Buffer
	writeWatchFrame(&got, "new frame\n")

	want := "\x1b[Hnew frame\n\x1b[J"
	if got.String() != want {
		t.Errorf("writeWatchFrame() = %q, want %q", got.String(), want)
	}
	if strings.Contains(got.String(), "\x1b[2J") {
		t.Errorf("writeWatchFrame() must not clear the screen before rendering: %q", got.String())
	}
}

func TestQueryAllCancelsInFlightProviders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	blocked := stubProvider{name: "blocked", fetch: func(ctx context.Context) (model.Snapshot, error) {
		close(started)
		<-ctx.Done()
		return model.Snapshot{}, ctx.Err()
	}}
	done := make(chan struct{})
	go func() {
		queryAll(ctx, []provider.Provider{blocked}, time.Now())
		close(done)
	}()
	<-started

	start := time.Now()
	cancel()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Fatalf("queryAll cancellation took %v, want <= 100ms", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("queryAll did not return after cancellation")
	}
}

func TestRunWatchWaitsFullIntervalAfterFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const interval = 25 * time.Millisecond
	var starts, finishes []time.Time

	runWatch(ctx, io.Discard, interval, true, func(context.Context) string {
		starts = append(starts, time.Now())
		time.Sleep(5 * time.Millisecond)
		finishes = append(finishes, time.Now())
		if len(starts) == 2 {
			cancel()
		}
		return "{}\n"
	})

	if len(starts) != 2 {
		t.Fatalf("frame starts = %d, want 2", len(starts))
	}
	if gap := starts[1].Sub(finishes[0]); gap < interval {
		t.Fatalf("completion-to-next-start gap = %v, want >= %v", gap, interval)
	}
}

func TestRunWatchRestoresCursorAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		runWatch(ctx, &out, time.Hour, false, func(ctx context.Context) string {
			close(started)
			<-ctx.Done()
			return ""
		})
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runWatch did not return after cancellation")
	}

	if got, want := out.String(), "\x1b[?25l\x1b[?25h\n"; got != want {
		t.Fatalf("watch terminal bytes = %q, want %q", got, want)
	}
}

func TestRunWatchInteractiveDebugFramesAreComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cache.Store{Path: filepath.Join(t.TempDir(), "cache.json")}
	providers := []provider.Provider{
		stubProvider{name: "claude", fetch: successfulFetch("claude")},
		stubProvider{name: "codex", fetch: successfulFetch("codex")},
	}
	var out bytes.Buffer
	frames := 0
	runWatch(ctx, &out, time.Millisecond, false, func(ctx context.Context) string {
		frames++
		if frames == 3 {
			cancel()
			return ""
		}
		return refreshInteractiveFrame(ctx, store, providers, []string{"claude", "codex"}, true)
	})

	got := out.String()
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"cursor hide", strings.Count(got, "\x1b[?25l"), 1},
		{"cursor show", strings.Count(got, "\x1b[?25h"), 1},
		{"cursor home", strings.Count(got, "\x1b[H"), 2},
		{"erase remainder", strings.Count(got, "\x1b[J"), 2},
		{"claude timing", strings.Count(got, "provider=claude "), 2},
		{"codex timing", strings.Count(got, "provider=codex "), 2},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s count = %d, want %d", check.name, check.got, check.want)
		}
	}
	if strings.Contains(got, "\x1b[2J") {
		t.Fatal("interactive debug watch must not pre-clear")
	}

	segments := strings.Split(got, "\x1b[H")[1:]
	for i, segment := range segments {
		erase := strings.Index(segment, "\x1b[J")
		claude := strings.Index(segment, "provider=claude ")
		codex := strings.Index(segment, "provider=codex ")
		if erase < 0 || claude < 0 || codex < 0 || claude > erase || codex > erase {
			t.Errorf("frame %d diagnostics are not contained in the complete frame", i+1)
		}
	}
}

func TestWriteWatchOutputJSONIsNDJSONWithoutANSI(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	results := []model.ProviderResult{model.Failed("claude", model.FailureUnavailable)}
	frame := jsonOutput(results, now, true)
	var out bytes.Buffer
	writeWatchOutput(&out, frame, true)
	writeWatchOutput(&out, frame, true)

	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("JSON watch output contains ANSI bytes: %q", out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSON watch records = %d, want 2", len(lines))
	}
	for i, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("record %d is not independently decodable: %v", i, err)
		}
	}
}

func TestOneShotJSONRemainsIndented(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	output := jsonOutput([]model.ProviderResult{model.Failed("claude", model.FailureUnavailable)}, now, false)
	if !strings.Contains(output, "\n  \"saved_at\"") {
		t.Fatalf("one-shot JSON is not indented: %q", output)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("one-shot JSON is invalid: %v", err)
	}
}

func TestAllProviderFailurePreservesPriorFreshness(t *testing.T) {
	store := &cache.Store{Path: filepath.Join(t.TempDir(), "cache.json")}
	priorSavedAt := time.Unix(1_700_000_000, 0)
	prior := []model.ProviderResult{model.Healthy(model.Snapshot{
		Provider: "claude",
		Session:  model.Window{Present: true, UsedPercent: 20},
	})}
	if err := store.Save(prior, priorSavedAt); err != nil {
		t.Fatalf("Save prior: %v", err)
	}
	failing := stubProvider{name: "claude", fetch: func(context.Context) (model.Snapshot, error) {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnavailable)
	}}

	refreshAndRender(context.Background(), store, []provider.Provider{failing}, []string{"claude"}, true, true, false, true)
	data, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after refresh: ok=%v err=%v", ok, err)
	}
	if !data.SavedAt.Equal(priorSavedAt) {
		t.Fatalf("SavedAt = %v, want preserved %v", data.SavedAt, priorSavedAt)
	}
}

func TestAllProviderFailureWithoutPriorIsCached(t *testing.T) {
	store := &cache.Store{Path: filepath.Join(t.TempDir(), "cache.json")}
	failing := stubProvider{name: "claude", fetch: func(context.Context) (model.Snapshot, error) {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnavailable)
	}}

	refreshAndRender(context.Background(), store, []provider.Provider{failing}, []string{"claude"}, true, true, false, true)
	data, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after refresh: ok=%v err=%v", ok, err)
	}
	if data.SavedAt.IsZero() || len(data.Results) != 1 || data.Results[0].Failure != model.FailureUnavailable {
		t.Fatalf("cached failure mismatch: %+v", data)
	}
}

func TestPartialSuccessAdvancesFreshnessAndPreservesOrder(t *testing.T) {
	store := &cache.Store{Path: filepath.Join(t.TempDir(), "cache.json")}
	priorSavedAt := time.Unix(1_700_000_000, 0)
	prior := []model.ProviderResult{
		model.Healthy(model.Snapshot{Provider: "claude", Session: model.Window{Present: true, UsedPercent: 20}}),
		model.Healthy(model.Snapshot{Provider: "codex", Weekly: model.Window{Present: true, UsedPercent: 30}}),
	}
	if err := store.Save(prior, priorSavedAt); err != nil {
		t.Fatalf("Save prior: %v", err)
	}
	providers := []provider.Provider{
		stubProvider{name: "claude", fetch: func(context.Context) (model.Snapshot, error) {
			return model.Snapshot{}, model.NewProviderError(model.FailureTimedOut)
		}},
		stubProvider{name: "codex", fetch: func(context.Context) (model.Snapshot, error) {
			return model.Snapshot{Provider: "codex", Weekly: model.Window{Present: true, UsedPercent: 45}}, nil
		}},
	}

	refreshAndRender(context.Background(), store, providers, []string{"claude", "codex"}, true, true, false, true)
	data, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after refresh: ok=%v err=%v", ok, err)
	}
	if !data.SavedAt.After(priorSavedAt) {
		t.Fatalf("SavedAt = %v, want after %v", data.SavedAt, priorSavedAt)
	}
	if len(data.Results) != 2 || data.Results[0].Snapshot.Provider != "claude" || data.Results[1].Snapshot.Provider != "codex" {
		t.Fatalf("provider order changed: %+v", data.Results)
	}
	if data.Results[0].Snapshot.Session.UsedPercent != 20 || data.Results[1].Snapshot.Weekly.UsedPercent != 45 {
		t.Fatalf("partial merge mismatch: %+v", data.Results)
	}
}

func BenchmarkQueryAllReadyProviders(b *testing.B) {
	providers := []provider.Provider{
		stubProvider{name: "claude", fetch: successfulFetch("claude")},
		stubProvider{name: "codex", fetch: successfulFetch("codex")},
	}
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	for b.Loop() {
		queryAll(context.Background(), providers, now)
	}
}

func successfulFetch(name string) func(context.Context) (model.Snapshot, error) {
	return func(context.Context) (model.Snapshot, error) {
		return model.Snapshot{Provider: name, Weekly: model.Window{Present: true}}, nil
	}
}
