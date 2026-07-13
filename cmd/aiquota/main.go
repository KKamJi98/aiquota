// Command aiquota renders a compact terminal card of the remaining Claude and
// Codex subscription quota (session and weekly windows) for the currently
// logged-in native CLIs. It never touches credentials directly; each provider
// adapter shells out to an installed CLI in a read-only mode and only normalized,
// non-sensitive fields are cached or displayed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/kkamji98/aiquota/internal/cache"
	"github.com/kkamji98/aiquota/internal/model"
	"github.com/kkamji98/aiquota/internal/provider"
	"github.com/kkamji98/aiquota/internal/provider/claude"
	"github.com/kkamji98/aiquota/internal/provider/codex"
	"github.com/kkamji98/aiquota/internal/render"
)

const defaultWidth = 80

type cliOptions struct {
	refresh  bool
	jsonOut  bool
	debug    bool
	noColor  bool
	watch    bool
	interval int
}

func main() {
	var options cliOptions
	registerFlags(flag.CommandLine, &options)
	flag.Parse()

	providers := []provider.Provider{claude.New(), codex.New()}
	order := providerNames(providers)

	store, err := cache.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aiquota: cannot locate cache dir")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Watch mode: keep the card on screen and auto-refresh until Ctrl-C.
	if options.watch {
		runWatch(ctx, os.Stdout, normalizeInterval(options.interval), options.jsonOut, func(ctx context.Context) string {
			if options.debug && !options.jsonOut {
				return refreshInteractiveFrame(ctx, store, providers, order, options.noColor)
			}
			return refreshAndRender(ctx, store, providers, order, options.jsonOut, options.noColor, options.debug, options.jsonOut)
		})
		return
	}

	// Fast path: a plain invocation with a fresh cache renders immediately.
	if !options.refresh {
		now := time.Now()
		if data, ok, _ := store.Load(); ok && data.Fresh(now) {
			emit(data.Results, now, data.SavedAt, options.jsonOut, options.noColor)
			if options.debug {
				fmt.Fprintf(os.Stderr, "cache hit, age=%s\n", render.FormatCountdown(data.Age(now)))
			}
			return
		}
	}

	refreshAndEmit(ctx, store, providers, order, options.jsonOut, options.noColor, options.debug)
}

// registerFlags binds long option names and their single-letter aliases.
func registerFlags(flags *flag.FlagSet, options *cliOptions) {
	flags.BoolVar(&options.refresh, "refresh", false, "query providers now instead of using cached data")
	flags.BoolVar(&options.refresh, "r", false, "shorthand for --refresh")
	flags.BoolVar(&options.jsonOut, "json", false, "print normalized results as JSON")
	flags.BoolVar(&options.jsonOut, "j", false, "shorthand for --json")
	flags.BoolVar(&options.debug, "debug", false, "print safe per-provider timing and failure categories to stderr")
	flags.BoolVar(&options.debug, "d", false, "shorthand for --debug")
	flags.BoolVar(&options.noColor, "no-color", false, "disable ANSI color")
	flags.BoolVar(&options.noColor, "n", false, "shorthand for --no-color")
	flags.BoolVar(&options.watch, "watch", false, "keep the card on screen and auto-refresh on an interval (Ctrl-C to exit)")
	flags.BoolVar(&options.watch, "w", false, "shorthand for --watch")
	flags.IntVar(&options.interval, "interval", 0, "watch refresh interval in seconds (default 60)")
	flags.IntVar(&options.interval, "i", 0, "shorthand for --interval")
}

// refreshAndEmit queries all providers concurrently, merges with the prior cache
// (keeping the last good value for any provider that failed this run), persists
// the result, and renders it.
func refreshAndEmit(ctx context.Context, store *cache.Store, providers []provider.Provider, order []string, jsonOut, noColor, debug bool) {
	_, _ = io.WriteString(os.Stdout, refreshAndRender(ctx, store, providers, order, jsonOut, noColor, debug, false))
}

type preparedRefresh struct {
	frame       string
	diagnostics string
}

// refreshAndRender queries all providers concurrently, merges with the prior
// cache (keeping the last good value for any provider that failed this run),
// persists the result, and returns a complete frame for the caller to render.
func refreshAndRender(ctx context.Context, store *cache.Store, providers []provider.Provider, order []string, jsonOut, noColor, debug, compactJSON bool) string {
	prepared := prepareRefresh(ctx, store, providers, order, jsonOut, noColor, debug, compactJSON)
	if prepared.diagnostics != "" {
		_, _ = io.WriteString(os.Stderr, prepared.diagnostics)
	}
	return prepared.frame
}

func refreshInteractiveFrame(ctx context.Context, store *cache.Store, providers []provider.Provider, order []string, noColor bool) string {
	prepared := prepareRefresh(ctx, store, providers, order, false, noColor, true, false)
	return prepared.frame + prepared.diagnostics
}

func prepareRefresh(ctx context.Context, store *cache.Store, providers []provider.Provider, order []string, jsonOut, noColor, debug, compactJSON bool) preparedRefresh {
	now := time.Now()
	fresh, timings := queryAll(ctx, providers, now)
	prior, priorOK, _ := store.Load()
	merged := cache.Merge(prior.Results, fresh, order)
	savedAt := now
	var diagnostics strings.Builder
	shouldSave := anySuccessful(fresh) || !priorOK || !anySuccessful(prior.Results)
	if shouldSave {
		if err := store.Save(merged, now); err != nil && debug {
			fmt.Fprintf(&diagnostics, "cache save failed: %v\n", err)
		}
	} else {
		savedAt = prior.SavedAt
	}

	if debug {
		for _, t := range timings {
			fmt.Fprintf(&diagnostics, "provider=%s elapsed=%s result=%s\n", t.name, t.elapsed, t.status)
		}
	}
	return preparedRefresh{
		frame:       renderOutput(merged, now, savedAt, jsonOut, noColor, compactJSON),
		diagnostics: diagnostics.String(),
	}
}

func anySuccessful(results []model.ProviderResult) bool {
	for _, result := range results {
		if result.OK() {
			return true
		}
	}
	return false
}

// normalizeInterval converts a --interval seconds value into a sane duration,
// defaulting to 60s and flooring at 2s so watch mode never busy-loops.
func normalizeInterval(sec int) time.Duration {
	const (
		def = 60
		min = 2
	)
	if sec <= 0 {
		sec = def
	}
	if sec < min {
		sec = min
	}
	return time.Duration(sec) * time.Second
}

// runWatch renders one frame immediately, then re-renders every interval until
// SIGINT/SIGTERM. It keeps the previous frame visible until the next one is
// ready, preventing slow provider queries from exposing a blank terminal.
// The cursor is hidden while watching and restored on exit.
func runWatch(ctx context.Context, w io.Writer, interval time.Duration, jsonOut bool, frame func(context.Context) string) {
	if !jsonOut {
		_, _ = io.WriteString(w, "\x1b[?25l")
		defer func() { _, _ = io.WriteString(w, "\x1b[?25h\n") }()
	}

	draw := func() bool {
		nextFrame := frame(ctx)
		if ctx.Err() != nil {
			return false
		}
		writeWatchOutput(w, nextFrame, jsonOut)
		return true
	}
	if !draw() {
		return
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !draw() {
				return
			}
			timer.Reset(interval)
		}
	}
}

func writeWatchOutput(w io.Writer, frame string, jsonOut bool) {
	if jsonOut {
		_, _ = io.WriteString(w, frame)
		return
	}
	writeWatchFrame(w, frame)
}

// writeWatchFrame updates the screen after a complete frame is ready. Erasing
// only after the frame preserves the prior frame while the next refresh runs.
func writeWatchFrame(w io.Writer, frame string) {
	_, _ = io.WriteString(w, "\x1b[H"+frame+"\x1b[J")
}

type timing struct {
	name    string
	elapsed time.Duration
	status  string
}

// queryAll runs every provider concurrently and returns normalized results plus
// safe timing info for --debug. Each adapter enforces its own hard timeout; the
// outer context is a generous backstop so one slow adapter cannot hang the CLI.
func queryAll(parent context.Context, providers []provider.Provider, now time.Time) ([]model.ProviderResult, []timing) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()

	results := make([]model.ProviderResult, len(providers))
	timings := make([]timing, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			start := time.Now()
			snap, err := p.Fetch(ctx)
			results[i] = toResult(p.Name(), snap, err, now)
			timings[i] = timing{name: p.Name(), elapsed: time.Since(start), status: statusOf(results[i])}
		}(i, p)
	}
	wg.Wait()
	return results, timings
}

// toResult normalizes a provider's (Snapshot, error) into a ProviderResult,
// mapping any non-ProviderError to the generic Unavailable category so raw error
// text never reaches output or cache.
func toResult(name string, snap model.Snapshot, err error, now time.Time) model.ProviderResult {
	if err != nil {
		var pe *model.ProviderError
		if errors.As(err, &pe) {
			return model.Failed(name, pe.Category)
		}
		return model.Failed(name, model.FailureUnavailable)
	}
	if snap.Provider == "" {
		snap.Provider = name
	}
	if snap.UpdatedAt.IsZero() {
		snap.UpdatedAt = now
	}
	return model.Healthy(snap)
}

func statusOf(r model.ProviderResult) string {
	if r.OK() {
		return "ok"
	}
	return string(r.Failure)
}

func providerNames(providers []provider.Provider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name()
	}
	return names
}

// emit writes the results either as JSON or as rendered ANSI cards.
func emit(results []model.ProviderResult, now, savedAt time.Time, jsonOut, noColor bool) {
	_, _ = io.WriteString(os.Stdout, renderOutput(results, now, savedAt, jsonOut, noColor, false))
}

// renderOutput prepares either JSON or ANSI card output without writing it.
func renderOutput(results []model.ProviderResult, now, savedAt time.Time, jsonOut, noColor, compactJSON bool) string {
	if jsonOut {
		return jsonOutput(results, savedAt, compactJSON)
	}
	color := colorEnabled(noColor)
	width := terminalWidth()
	return render.Render(results, width, color, now, savedAt)
}

// colorEnabled honors --no-color, the NO_COLOR convention, and whether stdout is
// a terminal.
func colorEnabled(noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return defaultWidth
}
