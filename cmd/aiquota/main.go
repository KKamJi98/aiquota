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
	"os"
	"sync"
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

func main() {
	var (
		refresh bool
		jsonOut bool
		debug   bool
		noColor bool
	)
	flag.BoolVar(&refresh, "refresh", false, "query providers now instead of using cached data")
	flag.BoolVar(&jsonOut, "json", false, "print normalized results as JSON")
	flag.BoolVar(&debug, "debug", false, "print safe per-provider timing and failure categories to stderr")
	flag.BoolVar(&noColor, "no-color", false, "disable ANSI color")
	flag.Parse()

	providers := []provider.Provider{claude.New(), codex.New()}
	order := providerNames(providers)

	store, err := cache.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aiquota: cannot locate cache dir")
		os.Exit(1)
	}

	now := time.Now()

	// Fast path: a plain invocation with a fresh cache renders immediately.
	if !refresh {
		if data, ok, _ := store.Load(); ok && data.Fresh(now) {
			emit(data.Results, now, data.SavedAt, jsonOut, noColor)
			if debug {
				fmt.Fprintf(os.Stderr, "cache hit, age=%s\n", render.FormatCountdown(data.Age(now)))
			}
			return
		}
	}

	// Refresh path: query all providers concurrently, then merge so that a
	// provider that failed this run keeps its last good cached value.
	fresh, timings := queryAll(providers, now)
	prior, _, _ := store.Load()
	merged := cache.Merge(prior.Results, fresh, order)
	if err := store.Save(merged, now); err != nil && debug {
		fmt.Fprintf(os.Stderr, "cache save failed: %v\n", err)
	}

	emit(merged, now, now, jsonOut, noColor)
	if debug {
		for _, t := range timings {
			fmt.Fprintf(os.Stderr, "provider=%s elapsed=%s result=%s\n", t.name, t.elapsed, t.status)
		}
	}
}

type timing struct {
	name    string
	elapsed time.Duration
	status  string
}

// queryAll runs every provider concurrently and returns normalized results plus
// safe timing info for --debug. Each adapter enforces its own hard timeout; the
// outer context is a generous backstop so one slow adapter cannot hang the CLI.
func queryAll(providers []provider.Provider, now time.Time) ([]model.ProviderResult, []timing) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
	if jsonOut {
		printJSON(results, savedAt)
		return
	}
	color := colorEnabled(noColor)
	width := terminalWidth()
	fmt.Print(render.Render(results, width, color, now, savedAt))
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
