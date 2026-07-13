package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

func TestLiveProbeSafeEvidence(t *testing.T) {
	if os.Getenv("AIQUOTA_INTEGRATION") != "1" {
		t.Skip("set AIQUOTA_INTEGRATION=1 to run the real Claude probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	raw, err := probe(ctx)
	if err != nil {
		t.Fatalf("probe failed before parsing: %T", err)
	}

	text := strings.ToLower(sanitize(raw))
	parseErr := validateHealthyProbe(raw)
	category := model.FailureNone
	var providerErr *model.ProviderError
	if errors.As(parseErr, &providerErr) {
		category = providerErr.Category
	}
	t.Logf(
		"safe stages: usage_seen=%t usage_echoes=%d session_seen=%t week_seen=%t percent_markers=%d reset_markers=%d trust_seen=%t already_running_seen=%t login_seen=%t parse_category=%s",
		strings.Contains(text, "usage"),
		strings.Count(text, "/usage"),
		strings.Contains(text, "current session"),
		strings.Contains(text, "current week"),
		strings.Count(text, "% used"),
		strings.Count(text, "resets"),
		strings.Contains(text, "trust"),
		strings.Contains(text, "already running"),
		strings.Contains(text, "log in") || strings.Contains(text, "sign in"),
		category,
	)
	if parseErr != nil {
		t.Fatalf("healthy Claude probe parse failed: category=%s type=%T", category, parseErr)
	}
}

func validateHealthyProbe(raw string) error {
	_, err := parseUsage(raw)
	return err
}

func TestValidateHealthyProbeRejectsUnsupported(t *testing.T) {
	err := validateHealthyProbe("Claude Code Usage")
	assertCategory(t, err, model.FailureUnsupported)
}

func TestFetchMissingExecutableReturnsPromptly(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	start := time.Now()
	_, err := New().Fetch(context.Background())
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("missing executable took %v, want < 500ms", elapsed)
	}
	assertCategory(t, err, model.FailureUnavailable)
}

func TestRunProbeCommandEarlyExitReturnsPromptly(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.Dir = t.TempDir()
	start := time.Now()
	_, err := runProbeCommand(context.Background(), cmd)
	if err == nil {
		t.Fatal("early-exiting probe command returned nil error")
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("early-exiting probe took %v, want < 500ms", elapsed)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("early-exiting child was not reaped: %+v", cmd.ProcessState)
	}
	assertCategory(t, safeProbeFailure(context.Background(), err), model.FailureUnavailable)
}

func TestProbePipelineEarlyClaudeExitReturnsPromptly(t *testing.T) {
	binDir := t.TempDir()
	homeDir := t.TempDir()
	claudePath := filepath.Join(binDir, "claude")
	scriptPath := filepath.Join(binDir, "script")
	pidFile := filepath.Join(t.TempDir(), "claude.pid")
	writeExecutable(t, claudePath, "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$AIQUOTA_TEST_PID_FILE\"\nexit 0\n")
	writeExecutable(t, scriptPath, "#!/bin/sh\nexec claude --disallowedTools '*'\n")
	t.Setenv("HOME", homeDir)
	t.Setenv("AIQUOTA_TEST_PID_FILE", pidFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/bin:/usr/bin")
	if err := exec.Command(scriptPath).Run(); err != nil {
		t.Fatalf("warm fake pipeline executable: %v", err)
	}
	if err := os.Remove(pidFile); err != nil {
		t.Fatalf("clear warmed fake Claude pid file: %v", err)
	}

	start := time.Now()
	_, err := New().Fetch(context.Background())
	elapsed := time.Since(start)
	t.Logf("real pipeline early exit latency=%v", elapsed)
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("real pipeline early exit took %v, want < 500ms", elapsed)
	}
	assertCategory(t, err, model.FailureUnavailable)

	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read timed fake Claude pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("parse timed fake Claude pid: %v", err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find timed fake Claude process: %v", err)
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("fake Claude process %d survived early exit", pid)
	} else if !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("fake Claude process %d signal check: %v", pid, err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func TestStopProbeGracefulReapsExactlyOnce(t *testing.T) {
	cmd, process := startControlledProbe(t, `trap 'exit 0' TERM; while :; do sleep 1; done`)
	start := time.Now()
	screen, err := stopProbe(process, "ready", nil)
	if err != nil || screen != "ready" {
		t.Fatalf("stopProbe() = (%q, %v), want (ready, nil)", screen, err)
	}
	assertReapedOnce(t, cmd, process.commandWait)
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("graceful shutdown took %v, want < 500ms", elapsed)
	}
}

func TestStopProbeSIGKILLFallbackReapsExactlyOnce(t *testing.T) {
	cmd, process := startControlledProbe(t, `trap '' TERM; while :; do sleep 1; done`)
	start := time.Now()
	_, err := stopProbe(process, "", context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stopProbe() error = %v, want context canceled", err)
	}
	assertReapedOnce(t, cmd, process.commandWait)
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("SIGKILL fallback took %v, want < 500ms", elapsed)
	}
}

func startControlledProbe(t *testing.T, script string) (*exec.Cmd, probeProcess) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start controlled probe: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	// Ensure the shell installed its signal trap before the test stops the group.
	time.Sleep(20 * time.Millisecond)
	return cmd, probeProcess{pgid: cmd.Process.Pid, commandWait: waitDone}
}

func assertReapedOnce(t *testing.T, cmd *exec.Cmd, waitDone <-chan error) {
	t.Helper()
	if cmd.ProcessState == nil {
		t.Fatalf("child was not reaped: %+v", cmd.ProcessState)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("reaped child still accepts signal 0: %v", err)
	}
	select {
	case extra := <-waitDone:
		t.Fatalf("unexpected second Wait result: %v", extra)
	default:
	}
}

func TestUsageScreenReady(t *testing.T) {
	if !usageScreenReady(healthyFixture) {
		t.Fatal("complete usage screen should be ready")
	}
	if usageScreenReady("Current session 20% used") {
		t.Fatal("partial usage screen must not be ready")
	}
	if !usageScreenReady("Please sign in to Claude to continue") {
		t.Fatal("recognized signed-out screen should be ready")
	}
}

func TestSyncBufferBoundsCapture(t *testing.T) {
	buffer := syncBuffer{maxBytes: 5}
	if n, err := buffer.Write([]byte("12345678")); err != nil || n != 8 {
		t.Fatalf("Write() = (%d, %v), want (8, nil)", n, err)
	}
	got, overflow := buffer.snapshot()
	if got != "12345" || !overflow {
		t.Fatalf("snapshot() = (%q, %v), want (%q, true)", got, overflow, "12345")
	}
}
