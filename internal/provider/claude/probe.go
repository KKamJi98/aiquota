// Package claude implements the aiquota Provider for a Claude subscription.
//
// The Claude CLI exposes no stable JSON quota command: the only place the
// session (5h) and weekly windows appear with percent-used and reset time is the
// interactive `/usage` view. This adapter therefore drives that view in a
// strictly read-only way and parses the resulting terminal text.
//
// Auth boundary (non-negotiable). This package NEVER reads, writes, refreshes,
// prints, caches, or transmits OAuth credentials, cookies, Keychain values, API
// keys, or credential files. It never triggers login/logout/token refresh, never
// opens a browser, and never sends a model prompt. It only spawns the installed
// `claude` CLI, has it render the built-in `/usage` slash command with tools
// disabled in a throwaway temp directory under a hard timeout, and consumes
// stdout/stderr. Any credential access is the CLI's own doing, not this code's.
package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

// fetchTimeout is the hard ceiling for a single Fetch. On expiry the child
// process group is killed and Fetch reports FailureTimedOut.
const fetchTimeout = 6 * time.Second

// Probe timing within the budget: let the TUI initialize, type `/usage`, submit
// it, then let the view render before capturing. Measured well under the 6s
// ceiling during the feasibility spike (~4s wall).
const (
	initDelay   = 1500 * time.Millisecond
	submitDelay = 400 * time.Millisecond
	renderDelay = 2000 * time.Millisecond
)

// Provider fetches Claude subscription quota via the `/usage` view.
type Provider struct{}

// New returns a Claude quota Provider.
func New() Provider { return Provider{} }

// Name is the stable provider identifier.
func (Provider) Name() string { return "claude" }

// Fetch drives the Claude CLI `/usage` view under a hard timeout and returns a
// normalized Snapshot, or a *model.ProviderError with a safe category.
func (Provider) Fetch(ctx context.Context) (model.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	raw, err := probe(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return model.Snapshot{}, model.NewProviderError(model.FailureTimedOut)
		}
		// Setup failures (no `claude`/`script` binary, temp dir, etc.) are
		// reported as unavailable with no raw detail.
		return model.Snapshot{}, model.NewProviderError(model.FailureUnavailable)
	}
	return parseUsage(raw)
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes the child's PTY
// output stream performs while the driver reads the deadline.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// probe spawns the Claude CLI inside a PTY (allocated cheaply via `script`),
// types the `/usage` command, captures the rendered view, and returns the raw
// terminal text. The CLI is run in a throwaway temp directory with tools
// disabled. The child is always killed at the end (the interactive view never
// exits on its own), so a kill is normal, not an error.
func probe(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "aiquota-claude-probe-")
	if err != nil {
		return "", fmt.Errorf("probe temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// `script -q /dev/null <cmd...>` (BSD/macOS form) allocates a PTY, discards
	// its own typescript, and runs the command attached to it. Tools are
	// disabled so no model/tool side effect is possible; only the client-side
	// `/usage` view is exercised.
	cmd := exec.Command("script", "-q", "/dev/null",
		"claude", "--disallowedTools", "*")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COLUMNS=120", "LINES=50")

	var out syncBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("probe stdin: %w", err)
	}
	// Own process group so the whole child tree (script + claude + node) can be
	// killed together; killing only `script` would orphan `claude`.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude probe: %w", err)
	}
	pgid := cmd.Process.Pid

	// Type `/usage` and submit it once the TUI has initialized, aborting if the
	// context ends first.
	go func() {
		if !sleepCtx(ctx, initDelay) {
			return
		}
		_, _ = stdin.Write([]byte("/usage"))
		if !sleepCtx(ctx, submitDelay) {
			return
		}
		_, _ = stdin.Write([]byte("\r"))
	}()

	// Wait for the render window or the context deadline, whichever comes first.
	captureDone := time.NewTimer(initDelay + submitDelay + renderDelay)
	defer captureDone.Stop()

	var ctxErr error
	select {
	case <-captureDone.C:
	case <-ctx.Done():
		ctxErr = ctx.Err()
	}

	// Kill the whole process group and reap it. A kill is the expected way this
	// interactive view ends, so cmd.Wait's error is intentionally ignored.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = stdin.Close()
	_ = cmd.Wait()

	if ctxErr != nil {
		return "", ctxErr
	}
	return out.String(), nil
}

// sleepCtx sleeps for d, returning false if ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
