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
// disabled from the user's home directory under a hard timeout, and consumes
// stdout/stderr. Any credential access is the CLI's own doing, not this code's.
package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/hinshun/vt10x"

	"github.com/kkamji98/aiquota/internal/model"
)

// Virtual terminal grid size. The child renders into a PTY of roughly this size,
// and the same size is used to replay the captured byte stream so the final
// screen is reconstructed faithfully.
const (
	probeCols       = 120
	probeRows       = 50
	maxCaptureBytes = 4 << 20
)

// fetchTimeout is the hard ceiling for a single Fetch. On expiry the child
// process group is killed and Fetch reports FailureTimedOut.
const fetchTimeout = 6 * time.Second

// Probe timing within the budget: let the TUI initialize, type `/usage`, submit
// it, and retry up to twice if an earlier command has not produced a parseable
// view. A ready view is detected and killed before the next retry fires. The
// capture fallback remains under the 6s ceiling.
const (
	initDelay      = 2000 * time.Millisecond
	submitDelay    = 400 * time.Millisecond
	retryDelay     = 1000 * time.Millisecond
	captureWindow  = 5800 * time.Millisecond
	readinessPoll  = 100 * time.Millisecond
	shutdownGrace  = 150 * time.Millisecond
	earlyExitGrace = 25 * time.Millisecond
	readySettle    = 250 * time.Millisecond
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
		return model.Snapshot{}, safeProbeFailure(ctx, err)
	}
	return parseUsage(raw)
}

func safeProbeFailure(ctx context.Context, _ error) *model.ProviderError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return model.NewProviderError(model.FailureTimedOut)
	}
	// Setup and child-process failures are unavailable with no raw detail.
	return model.NewProviderError(model.FailureUnavailable)
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes the child's PTY
// output stream performs while the driver reads the deadline.
type syncBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	maxBytes int
	overflow bool
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.maxBytes - b.buf.Len()
	if remaining <= 0 {
		b.overflow = true
		return n, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.buf.Write(p)
	return n, nil
}

func (b *syncBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.overflow
}

// probe drives the Claude CLI `/usage` view and returns the reconstructed screen
// text. The keystrokes are typed by a shell subprocess (printf into the PTY that
// `script` allocates) rather than written from Go: on util-linux `script`,
// input written from Go's side of the pipe/PTY does not register in the CLI,
// while a real shell writer does. The CLI runs from the user's home directory
// with tools disabled. The interactive view normally does not exit on its own,
// so readiness, timeout, and cancellation kill and reap the process group.
func probe(ctx context.Context) (string, error) {
	for _, name := range []string{"claude", "script"} {
		if _, err := exec.LookPath(name); err != nil {
			return "", fmt.Errorf("required executable %s unavailable", name)
		}
	}

	// Run in the user's home directory, not a fresh temp dir: the Claude CLI on
	// Linux does not accept slash-command input in a brand-new empty directory
	// (the `/usage` keystrokes never register), whereas it works in an existing
	// one. Home is outside any project, so no project CLAUDE.md/tools are loaded,
	// and tools are disabled anyway.
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("probe home dir: %w", err)
	}

	// Keep the proven real-shell input pipeline. If the script/Claude side exits,
	// the right side terminates the supervisor shell immediately instead of
	// waiting for the left-side writer's startup and retry sleeps.
	pipeline := fmt.Sprintf(
		`parent=$$; ( %s ) | { %s; status=$?; kill -TERM "$parent" 2>/dev/null; exit "$status"; }`,
		inputWriterScript(), scriptPipeline())
	cmd := exec.Command("sh", "-c", pipeline)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("COLUMNS=%d", probeCols),
		fmt.Sprintf("LINES=%d", probeRows),
		"TERM=xterm-256color")

	return runProbeCommand(ctx, cmd)
}

func runProbeCommand(ctx context.Context, cmd *exec.Cmd) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	out := syncBuffer{maxBytes: maxCaptureBytes}
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.WaitDelay = earlyExitGrace

	// Own one process group so the supervisor, writer, script, Claude, and their
	// descendants are stopped together on every completion path.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude probe: %w", err)
	}
	pgid := cmd.Process.Pid
	commandWait := make(chan error, 1)
	go func() {
		commandWait <- cmd.Wait()
	}()
	process := probeProcess{pgid: pgid, commandWait: commandWait}

	if err := ctx.Err(); err != nil {
		return stopProbe(process, "", err)
	}

	// Poll only after the command has been submitted. A complete parse is the
	// readiness signal; the fixed capture window remains a compatibility fallback
	// for safe unsupported classification when the view never becomes parseable.
	captureDone := time.NewTimer(captureWindow)
	defer captureDone.Stop()
	poll := time.NewTicker(readinessPoll)
	defer poll.Stop()
	startedAt := time.Now()

	for {
		select {
		case err := <-commandWait:
			if err != nil {
				return stopProbeAfterExit(process, true, "", fmt.Errorf("claude probe exited before ready: %w", err))
			}
			return stopProbeAfterExit(process, true, "", errors.New("claude probe exited before ready"))
		case <-captureDone.C:
			raw, overflow := out.snapshot()
			if overflow {
				return stopProbe(process, "", errors.New("claude probe capture exceeded safe limit"))
			}
			return stopProbe(process, reconstructScreen(raw), nil)
		case <-poll.C:
			if time.Since(startedAt) < initDelay+submitDelay {
				continue
			}
			raw, overflow := out.snapshot()
			if overflow {
				return stopProbe(process, "", errors.New("claude probe capture exceeded safe limit"))
			}
			candidate := reconstructScreen(raw)
			if usageScreenReady(candidate) {
				return stopProbe(process, candidate, nil)
			}
		case <-ctx.Done():
			return stopProbe(process, "", ctx.Err())
		}
	}
}

type probeProcess struct {
	pgid        int
	commandWait <-chan error
}

func stopProbe(process probeProcess, screen string, probeErr error) (string, error) {
	return stopProbeWithState(process, false, shutdownGrace, screen, probeErr)
}

func stopProbeAfterExit(process probeProcess, commandDone bool, screen string, probeErr error) (string, error) {
	return stopProbeWithState(process, commandDone, earlyExitGrace, screen, probeErr)
}

func stopProbeWithState(process probeProcess, commandDone bool, gracePeriod time.Duration, screen string, probeErr error) (string, error) {
	_ = syscall.Kill(-process.pgid, syscall.SIGTERM)
	grace := time.NewTimer(gracePeriod)
	defer grace.Stop()
	commandWait := process.commandWait
	if commandDone {
		commandWait = nil
	}
	graceC := grace.C
	for commandWait != nil {
		select {
		case <-commandWait:
			commandWait = nil
		case <-graceC:
			_ = syscall.Kill(-process.pgid, syscall.SIGKILL)
			graceC = nil
		}
	}
	if probeErr != nil {
		return "", probeErr
	}
	// Give the native CLI a small bounded window to release state that may outlive
	// the script parent before another rapid probe starts. Cancellation skips this
	// settle because prompt terminal restoration has priority.
	time.Sleep(readySettle)
	return screen, nil
}

func inputWriterScript() string {
	return fmt.Sprintf(
		`sleep %.2f; printf '/usage'; sleep %.2f; printf '\r'; sleep %.2f; printf '/usage'; sleep %.2f; printf '\r'; sleep %.2f; printf '/usage'; sleep %.2f; printf '\r'; sleep 30`,
		initDelay.Seconds(), submitDelay.Seconds(), retryDelay.Seconds(), submitDelay.Seconds(), retryDelay.Seconds(), submitDelay.Seconds())
}

func usageScreenReady(screen string) bool {
	_, err := parseUsage(screen)
	if err == nil {
		return true
	}
	var providerErr *model.ProviderError
	return errors.As(err, &providerErr) && providerErr.Category == model.FailureNotSignedIn
}

// reconstructScreen replays the captured PTY byte stream through a virtual
// terminal so the final rendered screen is read as a clean 2D grid. Scraping the
// raw stream directly loses column spacing (words merge) and includes redraw
// artifacts (dropped characters); replaying it into the grid resolves both.
func reconstructScreen(raw string) string {
	term := vt10x.New(vt10x.WithSize(probeCols, probeRows))
	_, _ = term.Write([]byte(raw))
	return term.String()
}

// scriptPipeline returns the platform-appropriate `script` invocation as a
// shell fragment that runs `claude` with tools disabled inside a PTY. BSD/macOS takes
// the command as trailing args (so `*` stays literal); util-linux takes it as a
// single `-c` shell string (so `*` is single-quoted against globbing).
func scriptPipeline() string {
	if runtime.GOOS == "darwin" {
		return `script -q /dev/null claude --disallowedTools '*'`
	}
	return `script -q -c "claude --disallowedTools '*'" /dev/null`
}
