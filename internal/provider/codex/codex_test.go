package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
	"github.com/kkamji98/aiquota/internal/provider"
)

var _ provider.Provider = New()

var fixtureTime = time.Unix(1_700_000_000, 0)

func TestMapRateLimitsHealthy(t *testing.T) {
	raw := decodeRateLimitFixture(t, `{
		"rateLimits": {
			"planType": "pro",
			"primary": {"usedPercent": 20, "windowDurationMins": 300, "resetsAt": 1700000300},
			"secondary": {"usedPercent": 40, "windowDurationMins": 10080, "resetsAt": 1700604800}
		}
	}`)

	snapshot, err := mapRateLimits(raw, fixtureTime)
	if err != nil {
		t.Fatalf("mapRateLimits() error = %v", err)
	}
	if snapshot.Provider != providerName || snapshot.Source != snapshotSource || snapshot.Plan != "pro" || !snapshot.UpdatedAt.Equal(fixtureTime) {
		t.Fatalf("snapshot metadata = %#v", snapshot)
	}
	assertWindow(t, snapshot.Session, 20, 1_700_000_300)
	assertWindow(t, snapshot.Weekly, 40, 1_700_604_800)
}

func TestMapRateLimitsSingleWindow(t *testing.T) {
	raw := decodeRateLimitFixture(t, `{
		"rateLimits": {
			"primary": {"usedPercent": 25, "windowDurationMins": 300, "resetsAt": 1700000300},
			"secondary": null
		}
	}`)

	snapshot, err := mapRateLimits(raw, fixtureTime)
	if err != nil {
		t.Fatalf("mapRateLimits() error = %v", err)
	}
	assertWindow(t, snapshot.Session, 25, 1_700_000_300)
	if snapshot.Weekly.Present {
		t.Fatalf("weekly window = %#v, want absent", snapshot.Weekly)
	}
}

func TestMapRateLimitsUsesDurationRatherThanPrimarySecondaryOrder(t *testing.T) {
	raw := decodeRateLimitFixture(t, `{
		"rateLimits": {
			"primary": {"usedPercent": 70, "windowDurationMins": 10080, "resetsAt": 1700604800},
			"secondary": {"usedPercent": 10, "windowDurationMins": 300, "resetsAt": 1700000300}
		}
	}`)

	snapshot, err := mapRateLimits(raw, fixtureTime)
	if err != nil {
		t.Fatalf("mapRateLimits() error = %v", err)
	}
	assertWindow(t, snapshot.Session, 10, 1_700_000_300)
	assertWindow(t, snapshot.Weekly, 70, 1_700_604_800)
}

func TestMapRateLimitsRejectsMissingAndUnsupportedShapes(t *testing.T) {
	missing := decodeRateLimitFixture(t, `{"rateLimits": null}`)
	assertFailureCategory(t, func() error {
		_, err := mapRateLimits(missing, fixtureTime)
		return err
	}, model.FailureUnsupported)

	unsupported := decodeRateLimitFixture(t, `{
		"rateLimits": {
			"primary": {"usedPercent": 10, "windowDurationMins": 300, "resetsAt": 1700000300},
			"secondary": {"usedPercent": 20, "windowDurationMins": 600, "resetsAt": 1700000600}
		}
	}`)
	assertFailureCategory(t, func() error {
		_, err := mapRateLimits(unsupported, fixtureTime)
		return err
	}, model.FailureUnsupported)
}

func TestAccountReadNotSignedIn(t *testing.T) {
	var raw accountReadResult
	if err := json.Unmarshal([]byte(`{"account": null, "requiresOpenaiAuth": true}`), &raw); err != nil {
		t.Fatalf("decode synthetic account fixture: %v", err)
	}
	if raw.signedIn() {
		t.Fatal("signedIn() = true, want false")
	}
}

func TestFetchCancellationKillsAppServerProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are Unix-specific")
	}

	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "child.pid")
	commandPath := filepath.Join(tempDir, "codex")
	command := "#!/bin/sh\nsleep 30 &\necho $! > \"$AIQUOTA_TEST_PID_FILE\"\nwait\n"
	if err := os.WriteFile(commandPath, []byte(command), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AIQUOTA_TEST_PID_FILE", pidFile)
	resolvedCommand, err := exec.LookPath("codex")
	if err != nil || resolvedCommand != commandPath {
		t.Fatalf("LookPath(codex) = %q, %v, want %q", resolvedCommand, err, commandPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := New().Fetch(ctx)
		result <- err
	}()

	var pidBytes []byte
	deadline := time.Now().Add(time.Second)
	for {
		pidBytes, err = os.ReadFile(pidFile)
		if err == nil && strings.TrimSpace(string(pidBytes)) != "" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("fake codex child did not publish its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	startedAt := time.Now()
	cancel()
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("Fetch() did not return within 1s of cancellation")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("Fetch() cancellation elapsed = %v, want <= 1s", elapsed)
	}
	assertFailureCategory(t, func() error { return err }, model.FailureUnavailable)

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	childDeadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatalf("check child process %d: %v", pid, err)
		}
		if time.Now().After(childDeadline) {
			t.Fatalf("child process %d still exists after cancellation", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodeRateLimitFixture(t *testing.T, fixture string) *rawRateLimits {
	t.Helper()
	var response rateLimitsReadResult
	if err := json.Unmarshal([]byte(fixture), &response); err != nil {
		t.Fatalf("decode synthetic rate-limit fixture: %v", err)
	}
	return response.RateLimits
}

func assertWindow(t *testing.T, got model.Window, usedPercent int, resetsAt int64) {
	t.Helper()
	if !got.Present || got.UsedPercent != usedPercent || got.ResetsAt.Unix() != resetsAt {
		t.Fatalf("window = %#v, want present=%t usedPercent=%d resetsAt=%d", got, true, usedPercent, resetsAt)
	}
}

func assertFailureCategory(t *testing.T, call func() error, want model.FailureCategory) {
	t.Helper()
	var providerError *model.ProviderError
	if err := call(); !errors.As(err, &providerError) || providerError.Category != want {
		t.Fatalf("error = %v, want ProviderError(%q)", err, want)
	}
}
