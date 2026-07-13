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
	probeCols = 120
	probeRows = 50
)

// fetchTimeout is the hard ceiling for a single Fetch. On expiry the child
// process group is killed and Fetch reports FailureTimedOut.
const fetchTimeout = 6 * time.Second

// Probe timing within the budget: let the TUI initialize, type `/usage`, submit
// it, then let the view render before capturing. Total stays under the 6s
// ceiling (1.5 + 0.4 + 2.5 + 0.5 margin = 4.9s).
const (
	initDelay   = 1500 * time.Millisecond
	submitDelay = 400 * time.Millisecond
	renderDelay = 2500 * time.Millisecond
	killMargin  = 500 * time.Millisecond
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

// probe drives the Claude CLI `/usage` view and returns the reconstructed screen
// text. The keystrokes are typed by a shell subprocess (printf into the PTY that
// `script` allocates) rather than written from Go: on util-linux `script`,
// input written from Go's side of the pipe/PTY does not register in the CLI,
// while a real shell writer does. The CLI runs in a throwaway temp directory with
// tools disabled. The interactive view never exits on its own, so the process
// group is always killed at the end; a kill is the normal termination here.
func probe(ctx context.Context) (string, error) {
	// Run in the user's home directory, not a fresh temp dir: the Claude CLI on
	// Linux does not accept slash-command input in a brand-new empty directory
	// (the `/usage` keystrokes never register), whereas it works in an existing
	// one. Home is outside any project, so no project CLAUDE.md/tools are loaded,
	// and tools are disabled anyway.
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("probe home dir: %w", err)
	}

	// A subshell types `/usage` + Enter into the PTY, then idles; the outer kill
	// stops capture. `printf '\r'` submits the command.
	pipeline := fmt.Sprintf(
		`( sleep %.2f; printf '/usage'; sleep %.2f; printf '\r'; sleep 30 ) | %s`,
		initDelay.Seconds(), submitDelay.Seconds(), scriptPipeline())

	cmd := exec.Command("sh", "-c", pipeline)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("COLUMNS=%d", probeCols),
		fmt.Sprintf("LINES=%d", probeRows),
		"TERM=xterm-256color")

	var out syncBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// Own process group so the whole child tree (sh + script + claude + node) is
	// killed together; killing only `sh` would orphan the rest.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude probe: %w", err)
	}
	pgid := cmd.Process.Pid

	// Capture through init + submit + render, then a small margin, or stop early
	// if the caller's context ends first.
	captureDone := time.NewTimer(initDelay + submitDelay + renderDelay + killMargin)
	defer captureDone.Stop()

	var ctxErr error
	select {
	case <-captureDone.C:
	case <-ctx.Done():
		ctxErr = ctx.Err()
	}

	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = cmd.Wait()

	if ctxErr != nil {
		return "", ctxErr
	}
	return reconstructScreen(out.String()), nil
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

// scriptPipeline returns the platform-appropriate `script` invocation (as a shell
// fragment) that runs `claude` with tools disabled inside a PTY. BSD/macOS takes
// the command as trailing args (so `*` stays literal); util-linux takes it as a
// single `-c` shell string (so `*` is single-quoted against globbing).
func scriptPipeline() string {
	if runtime.GOOS == "darwin" {
		return `script -q /dev/null claude --disallowedTools '*'`
	}
	return `script -q -c "claude --disallowedTools '*'" /dev/null`
}
